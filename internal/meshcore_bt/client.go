package meshcore_bt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

// ============================================================================
// Основной клиент
// ============================================================================

type MeshCoreClient struct {
	adapter   *bluetooth.Adapter
	device    *bluetooth.Device
	rxChar    *bluetooth.DeviceCharacteristic
	txChar    *bluetooth.DeviceCharacteristic
	connected bool
	mu        sync.RWMutex

	// Состояние
	selfInfo    *SelfInfo
	contacts    map[string]*Contact
	channels    map[int]*Channel
	messages    map[string][]*Message // contactKey -> messages
	channelMsgs map[int][]*ChannelMessage

	// Каналы для обработки
	notifyCh chan []byte
	doneCh   chan struct{}

	// Коллбэки
	onContact        func(*Contact)
	onChannel        func(*Channel)
	onMessage        func(*Message)
	onChannelMessage func(*ChannelMessage)
	onSelfInfo       func(*SelfInfo)
	onStats          func(*CompanionRadioStats)
	onBattery        func(millivolts int, usedKb, totalKb int)
	onError          func(error)
}

func NewMeshCoreClient(adapter *bluetooth.Adapter) *MeshCoreClient {
	return &MeshCoreClient{
		adapter:     adapter,
		contacts:    make(map[string]*Contact),
		channels:    make(map[int]*Channel),
		messages:    make(map[string][]*Message),
		channelMsgs: make(map[int][]*ChannelMessage),
		notifyCh:    make(chan []byte, 100),
		doneCh:      make(chan struct{}),
	}
}

// ============================================================================
// Управление подключением
// ============================================================================

func (c *MeshCoreClient) Connect(address bluetooth.Address) error {
	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return nil
	}

	device, err := c.adapter.Connect(address, bluetooth.ConnectionParams{})
	if err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}
	c.device = &device
	fmt.Println("Connected to device")

	services, err := device.DiscoverServices(nil)
	if err != nil {
		device.Disconnect()
		return fmt.Errorf("discover services failed: %w", err)
	}
	var targetService *bluetooth.DeviceService
	for _, svc := range services {
		if svc.UUID().String() == serviceUUID {
			targetService = &svc
			break
		}
	}
	if targetService == nil {
		device.Disconnect()
		return fmt.Errorf("meshcore service not found")
	}
	fmt.Println("Found MeshCore service")

	chars, err := targetService.DiscoverCharacteristics(nil)
	if err != nil {
		device.Disconnect()
		return fmt.Errorf("discover characteristics failed: %w", err)
	}
	for _, ch := range chars {
		uuid := ch.UUID().String()
		switch uuid {
		case rxCharacteristicUUID:
			c.rxChar = &ch // теперь корректно
			fmt.Println("Found RX characteristic")
		case txCharacteristicUUID:
			c.txChar = &ch // теперь корректно
			fmt.Println("Found TX characteristic")
		}
	}

	if c.rxChar == nil || c.txChar == nil {
		device.Disconnect()
		return fmt.Errorf("RX/TX characteristics not found")
	}

	fmt.Println("Enabling notifications on TX characteristic...")
	if err := c.txChar.EnableNotifications(func(data []byte) {
		fmt.Printf("Notification received: %x\n", data)
		select {
		case c.notifyCh <- data:
		default:
		}
	}); err != nil {
		fmt.Printf("EnableNotifications error: %v\n", err)
		device.Disconnect()
		return fmt.Errorf("enable notifications failed: %w", err)
	}
	fmt.Println("Notifications enabled")
	time.Sleep(500 * time.Millisecond) // даём время на активацию

	c.connected = true
	go c.frameHandler()
	c.mu.Unlock() // освобождаем мьютекс, чтобы write не блокировался

	fmt.Printf("%%+v Output: %+v\n", c)
	// Отправка инициализационных команд
	if err := c.sendAppStart(); err != nil {
		c.Disconnect()
		return err
	}
	time.Sleep(200 * time.Millisecond)

	if err := c.sendDeviceQuery(); err != nil {
		c.Disconnect()
		return err
	}
	return nil

	// // Повторная отправка AppStart
	// go func() {
	// 	ticker := time.NewTicker(2 * time.Second)
	// 	defer ticker.Stop()
	// 	for {
	// 		select {
	// 		case <-ticker.C:
	// 			c.mu.RLock()
	// 			if c.selfInfo != nil {
	// 				c.mu.RUnlock()
	// 				return
	// 			}
	// 			c.mu.RUnlock()
	// 			fmt.Println("Retrying AppStart")
	// 			_ = c.sendAppStart()
	// 		case <-c.doneCh:
	// 			return
	// 		}
	// 	}
	// }()

	// return nil
}

func (c *MeshCoreClient) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return nil
	}
	c.connected = false
	close(c.doneCh)
	if c.device != nil {
		return c.device.Disconnect()
	}
	return nil
}

// ============================================================================
// Отправка команд (сборка фреймов)
// ============================================================================

func (c *MeshCoreClient) sendAppStart() error {
	// [cmd][app_ver][reserved 6][app_name]\0
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdAppStart)
	buf.WriteByte(1)                    // appVersion = 1, как в Dart
	buf.Write([]byte{0, 0, 0, 0, 0, 0}) // reserved 6 bytes
	buf.WriteString("MeshCoreOpen")
	buf.WriteByte(0)
	data := buf.Bytes()
	fmt.Printf("Sending AppStart: %x\n", data)
	return c.write(data)
}

func (c *MeshCoreClient) sendDeviceQuery() error {
	data := []byte{cmdDeviceQuery, appProtocolVersion}
	fmt.Printf("Sending DeviceQuery: %x\n", data)
	return c.write(data)
}

func (c *MeshCoreClient) sendGetContacts(since uint32) error {
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdGetContacts)
	if since > 0 {
		_ = binary.Write(buf, binary.LittleEndian, since)
	}
	return c.write(buf.Bytes())
}

func (c *MeshCoreClient) sendGetChannel(index byte) error {
	return c.write([]byte{cmdGetChannel, index})
}

func (c *MeshCoreClient) sendSetChannel(index byte, name string, psk []byte) error {
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdSetChannel)
	buf.WriteByte(index)
	// имя: 32 байта, null-терминированное
	nameBytes := []byte(name)
	if len(nameBytes) > 31 {
		nameBytes = nameBytes[:31]
	}
	buf.Write(nameBytes)
	for i := len(nameBytes); i < 32; i++ {
		buf.WriteByte(0)
	}
	// PSK: 16 байт, нули если короче
	pskPadded := make([]byte, 16)
	copy(pskPadded, psk)
	buf.Write(pskPadded)
	return c.write(buf.Bytes())
}

func (c *MeshCoreClient) sendTextMessage(recipient []byte, text string, attempt byte, timestamp uint32) error {
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdSendTxtMsg)
	buf.WriteByte(txtTypePlain)
	buf.WriteByte(attempt)
	_ = binary.Write(buf, binary.LittleEndian, timestamp)
	// 6-байтовый префикс публичного ключа
	if len(recipient) < 6 {
		return fmt.Errorf("recipient key too short")
	}
	buf.Write(recipient[:6])
	buf.WriteString(text)
	buf.WriteByte(0)
	return c.write(buf.Bytes())
}

func (c *MeshCoreClient) sendChannelTextMessage(channelIndex byte, text string) error {
	timestamp := uint32(time.Now().Unix())
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdSendChannelTxtMsg)
	buf.WriteByte(txtTypePlain)
	buf.WriteByte(channelIndex)
	_ = binary.Write(buf, binary.LittleEndian, timestamp)
	buf.WriteString(text)
	buf.WriteByte(0)
	return c.write(buf.Bytes())
}

func (c *MeshCoreClient) sendSetDeviceTime(timestamp uint32) error {
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdSetDeviceTime)
	_ = binary.Write(buf, binary.LittleEndian, timestamp)
	return c.write(buf.Bytes())
}

func (c *MeshCoreClient) sendGetBatteryAndStorage() error {
	return c.write([]byte{cmdGetBattAndStorage})
}

func (c *MeshCoreClient) sendGetStats(statsType byte) error {
	return c.write([]byte{cmdGetStats, statsType})
}

func (c *MeshCoreClient) sendSetAdvertName(name string) error {
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdSetAdvertName)
	nameBytes := []byte(name)
	if len(nameBytes) > maxNameSize-1 {
		nameBytes = nameBytes[:maxNameSize-1]
	}
	buf.Write(nameBytes)
	buf.WriteByte(0)
	return c.write(buf.Bytes())
}

func (c *MeshCoreClient) sendSetAdvertLatLon(lat, lon float64) error {
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdSetAdvertLatLon)
	_ = binary.Write(buf, binary.LittleEndian, int32(lat*1e6))
	_ = binary.Write(buf, binary.LittleEndian, int32(lon*1e6))
	return c.write(buf.Bytes())
}

func (c *MeshCoreClient) sendResetPath(pubKey []byte) error {
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdResetPath)
	buf.Write(pubKey)
	return c.write(buf.Bytes())
}

func (c *MeshCoreClient) sendUpdateContact(contact *Contact) error {
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdAddUpdateContact)
	buf.Write(contact.PublicKey)
	buf.WriteByte(byte(contact.Type))
	buf.WriteByte(byte(contact.Flags))
	pathLen := len(contact.Path)
	if pathLen > 255 {
		pathLen = 255
	}
	buf.WriteByte(byte(pathLen))
	// Путь: 64 байта
	pathPadded := make([]byte, 64)
	copy(pathPadded, contact.Path)
	buf.Write(pathPadded)
	// Имя: 32 байта null-terminated
	nameBytes := []byte(contact.Name)
	if len(nameBytes) > 31 {
		nameBytes = nameBytes[:31]
	}
	buf.Write(nameBytes)
	for i := len(nameBytes); i < 32; i++ {
		buf.WriteByte(0)
	}
	// Таймстемп
	_ = binary.Write(buf, binary.LittleEndian, uint32(time.Now().Unix()))
	// Координаты (если есть)
	latInt, lonInt := int32(0), int32(0)
	if contact.Latitude != nil && contact.Longitude != nil {
		latInt = int32(*contact.Latitude * 1e6)
		lonInt = int32(*contact.Longitude * 1e6)
	}
	_ = binary.Write(buf, binary.LittleEndian, latInt)
	_ = binary.Write(buf, binary.LittleEndian, lonInt)
	// last_modified (optional, пока 0)
	_ = binary.Write(buf, binary.LittleEndian, uint32(0))
	return c.write(buf.Bytes())
}

func (c *MeshCoreClient) sendRemoveContact(pubKey []byte) error {
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdRemoveContact)
	buf.Write(pubKey)
	return c.write(buf.Bytes())
}

func (c *MeshCoreClient) sendSyncNextMessage() error {
	return c.write([]byte{cmdSyncNextMessage})
}

func (c *MeshCoreClient) sendReboot() error {
	return c.write([]byte{cmdReboot, 'r', 'e', 'b', 'o', 'o', 't'})
}

func (c *MeshCoreClient) write(data []byte) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.connected || c.rxChar == nil {
		return fmt.Errorf("not connected")
	}
	_, err := c.rxChar.Write(data)
	return err
}

// ============================================================================
// Обработчик входящих фреймов
// ============================================================================

func (c *MeshCoreClient) frameHandler() {
	for {
		select {
		case data := <-c.notifyCh:
			fmt.Printf("RX frame: %x\n", data)
			c.handleFrame(data)
		case <-c.doneCh:
			return
		}
	}
}

func (c *MeshCoreClient) handleFrame(data []byte) {
	if len(data) == 0 {
		return
	}
	code := data[0]
	fmt.Printf("handleFrame: code=0x%02x, len=%d\n", code, len(data))
	fmt.Printf("handleFrame: code=0x%02x\n", code)
	switch code {
	case respCodeSelfInfo:
		c.parseSelfInfo(data)
	case respCodeDeviceInfo:
		c.parseDeviceInfo(data)
	case respCodeContactsStart:
		// Начало списка контактов
	case respCodeContact:
		c.parseContact(data)
	case respCodeEndOfContacts:
		// Конец списка
	case respCodeChannelInfo:
		c.parseChannelInfo(data)
	case respCodeContactMsgRecv, respCodeContactMsgRecvV3:
		c.parseContactMessage(data)
	case respCodeChannelMsgRecv, respCodeChannelMsgRecvV3:
		c.parseChannelMessage(data)
	case respCodeSent:
		c.parseSent(data)
	case respCodeNoMoreMessages:
		// Обработка окончания очереди
	case pushCodeSendConfirmed:
		c.parseSendConfirmed(data)
	case pushCodePathUpdated:
		c.parsePathUpdated(data)
	case pushCodeMsgWaiting:
		// Есть сообщения в очереди, запросить
		_ = c.sendSyncNextMessage()
	case respCodeBattAndStorage:
		c.parseBatteryAndStorage(data)
	case respCodeStats:
		c.parseStats(data)
	case respCodeAutoAddConfig:
		c.parseAutoAddConfig(data)
	case respCodeCustomVars:
		c.parseCustomVars(data)
	case respCodeOk:
		// OK – можно использовать для синхронизации
	case respCodeErr:
		// Ошибка, можно прочитать код ошибки
	default:
		// Неизвестный код
	}
}

// ============================================================================
// Парсеры ответов
// ============================================================================

func (c *MeshCoreClient) parseSelfInfo(data []byte) {
	fmt.Printf("parseSelfInfo called with %d bytes\n", len(data))
	if len(data) < 59 {
		return
	}
	info := &SelfInfo{}
	r := bytes.NewReader(data)
	// пропускаем код (1 байт) и тип (1 байт)
	_, _ = r.Seek(2, 0)
	// tx_power, max_tx_power
	var tx, maxTx int8
	_ = binary.Read(r, binary.LittleEndian, &tx)
	_ = binary.Read(r, binary.LittleEndian, &maxTx)
	info.TxPower = tx
	info.MaxTxPower = maxTx

	// public key
	info.PublicKey = make([]byte, 32)
	_, _ = r.Read(info.PublicKey)

	// lat, lon
	var latInt, lonInt int32
	_ = binary.Read(r, binary.LittleEndian, &latInt)
	_ = binary.Read(r, binary.LittleEndian, &lonInt)
	info.Latitude = float64(latInt) / 1e6
	info.Longitude = float64(lonInt) / 1e6

	// multi_acks, advert_loc_policy, telemetry
	var multiAcks, advLocPolicy, teleFlags byte
	_ = binary.Read(r, binary.LittleEndian, &multiAcks)
	_ = binary.Read(r, binary.LittleEndian, &advLocPolicy)
	_ = binary.Read(r, binary.LittleEndian, &teleFlags)
	info.MultiAcks = int(multiAcks)
	info.AdvertLocPolicy = int(advLocPolicy)
	info.TeleModeBase = int(teleFlags & 0x03)
	info.TeleModeLoc = int((teleFlags >> 2) & 0x03)
	info.TeleModeEnv = int((teleFlags >> 4) & 0x03)

	var manualAdd byte
	_ = binary.Read(r, binary.LittleEndian, &manualAdd)
	info.ManualAdd = manualAdd == 0

	var freqHz uint32
	_ = binary.Read(r, binary.LittleEndian, &freqHz)
	info.FreqHz = freqHz

	var bwHz uint32
	_ = binary.Read(r, binary.LittleEndian, &bwHz)
	info.BwHz = bwHz

	var sf, cr byte
	_ = binary.Read(r, binary.LittleEndian, &sf)
	_ = binary.Read(r, binary.LittleEndian, &cr)
	info.Sf = int(sf)
	info.Cr = int(cr)

	// Имя (до конца, null-terminated)
	nameBytes := make([]byte, 0)
	for {
		b, err := r.ReadByte()
		if err != nil || b == 0 {
			break
		}
		nameBytes = append(nameBytes, b)
	}
	info.Name = string(nameBytes)

	c.mu.Lock()
	c.selfInfo = info
	c.mu.Unlock()
	if c.onSelfInfo != nil {
		c.onSelfInfo(info)
	}
}

func (c *MeshCoreClient) parseDeviceInfo(data []byte) {
	if len(data) < 4 {
		return
	}
	// firmware_ver_code = data[1]
	// max_contacts = data[2] * 2
	// max_channels = data[3]
	// client_repeat = data[80] (если есть)
	// path_hash_mode = data[81] (если есть)
	// можно сохранить в структуре устройства
}

func (c *MeshCoreClient) parseContact(data []byte) {
	if len(data) < contactFrameSize {
		return
	}
	pubKey := data[1:33]
	contactType := int(data[33])
	flags := int(data[34])
	pathLen := int(data[35])
	path := data[36 : 36+pathLen] // первые pathLen байт из 64
	nameBytes := data[100:132]
	name := string(bytes.TrimRight(nameBytes, "\x00"))
	lastSeen := time.Unix(int64(binary.LittleEndian.Uint32(data[132:136])), 0)
	latInt := int32(binary.LittleEndian.Uint32(data[136:140]))
	lonInt := int32(binary.LittleEndian.Uint32(data[140:144]))
	var lat, lon *float64
	if latInt != 0 || lonInt != 0 {
		latVal := float64(latInt) / 1e6
		lonVal := float64(lonInt) / 1e6
		lat = &latVal
		lon = &lonVal
	}
	contact := &Contact{
		PublicKey:  pubKey,
		Name:       name,
		Type:       contactType,
		Flags:      flags,
		PathLength: pathLen,
		Path:       path,
		Latitude:   lat,
		Longitude:  lon,
		LastSeen:   lastSeen,
	}
	c.mu.Lock()
	c.contacts[pubKeyHex(pubKey)] = contact
	c.mu.Unlock()
	if c.onContact != nil {
		c.onContact(contact)
	}
}

func (c *MeshCoreClient) parseChannelInfo(data []byte) {
	if len(data) < 2 {
		return
	}
	index := int(data[1])
	nameBytes := data[2:]
	nullIdx := bytes.IndexByte(nameBytes, 0)
	if nullIdx == -1 {
		nullIdx = len(nameBytes)
	}
	name := string(nameBytes[:nullIdx])
	// PSK находится после имени, но для простоты не парсим
	ch := &Channel{
		Index:   index,
		Name:    name,
		IsEmpty: len(name) == 0,
	}
	c.mu.Lock()
	c.channels[index] = ch
	c.mu.Unlock()
	if c.onChannel != nil {
		c.onChannel(ch)
	}
}

func (c *MeshCoreClient) parseContactMessage(data []byte) {
	// Упрощённый парсинг, можно расширить
	// Аналогично parseContactMessageText из Dart
	msg := &Message{
		Timestamp:  time.Now(),
		IsOutgoing: false,
	}
	// реализация парсинга опущена для краткости
	if c.onMessage != nil {
		c.onMessage(msg)
	}
}

func (c *MeshCoreClient) parseChannelMessage(data []byte) {
	// Аналогично
}

func (c *MeshCoreClient) parseSent(data []byte) {
	// Обработка подтверждения отправки
}

func (c *MeshCoreClient) parseSendConfirmed(data []byte) {
	// ACK от получателя
}

func (c *MeshCoreClient) parsePathUpdated(data []byte) {
	// Обновление пути контакта
}

func (c *MeshCoreClient) parseBatteryAndStorage(data []byte) {
	if len(data) < 11 {
		return
	}
	mv := binary.LittleEndian.Uint16(data[1:3])
	usedKb := binary.LittleEndian.Uint32(data[3:7])
	totalKb := binary.LittleEndian.Uint32(data[7:11])
	if c.onBattery != nil {
		c.onBattery(int(mv), int(usedKb), int(totalKb))
	}
}

func (c *MeshCoreClient) parseStats(data []byte) {
	if len(data) < 9 {
		return
	}
	stats := &CompanionRadioStats{}
	stats.TxAirSecs = binary.LittleEndian.Uint32(data[1:5])
	stats.RxAirSecs = binary.LittleEndian.Uint32(data[5:9])
	if len(data) > 9 {
		stats.NoiseFloorDbm = int8(data[9])
	}
	// SNR можно извлечь из следующего байта
	if len(data) > 10 {
		stats.LastSnrDb = float32(int8(data[10])) / 4.0
	}
	if c.onStats != nil {
		c.onStats(stats)
	}
}

func (c *MeshCoreClient) parseAutoAddConfig(data []byte) {
	// флаги в data[1]
}

func (c *MeshCoreClient) parseCustomVars(data []byte) {
	// строка в data[1:]
}

// ============================================================================
// Публичные методы API
// ============================================================================

func (c *MeshCoreClient) GetSelfInfo() *SelfInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.selfInfo
}

func (c *MeshCoreClient) GetContacts() map[string]*Contact {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// возвращаем копию
	res := make(map[string]*Contact)
	for k, v := range c.contacts {
		res[k] = v
	}
	return res
}

func (c *MeshCoreClient) GetChannels() map[int]*Channel {
	c.mu.RLock()
	defer c.mu.RUnlock()
	res := make(map[int]*Channel)
	for k, v := range c.channels {
		res[k] = v
	}
	return res
}

func (c *MeshCoreClient) SyncTime() error {
	now := uint32(time.Now().Unix())
	return c.sendSetDeviceTime(now)
}

func (c *MeshCoreClient) RequestContacts(since uint32) error {
	return c.sendGetContacts(since)
}

func (c *MeshCoreClient) RequestChannels(maxChannels int) error {
	for i := 0; i < maxChannels; i++ {
		if err := c.sendGetChannel(byte(i)); err != nil {
			return err
		}
	}
	return nil
}

func (c *MeshCoreClient) SendMessage(contact *Contact, text string) error {
	timestamp := uint32(time.Now().Unix())
	return c.sendTextMessage(contact.PublicKey, text, 0, timestamp)
}

func (c *MeshCoreClient) SendChannelMessage(channel *Channel, text string) error {
	return c.sendChannelTextMessage(byte(channel.Index), text)
}

func (c *MeshCoreClient) SetContactPath(contact *Contact, path []byte, pathLen int) error {
	// Создаём копию с новым путём
	updated := *contact
	updated.Path = path
	updated.PathLength = pathLen
	return c.sendUpdateContact(&updated)
}

func (c *MeshCoreClient) ResetContactPath(contact *Contact) error {
	return c.sendResetPath(contact.PublicKey)
}

func (c *MeshCoreClient) RemoveContact(contact *Contact) error {
	return c.sendRemoveContact(contact.PublicKey)
}

func (c *MeshCoreClient) SetChannel(channelIndex int, name string, psk []byte) error {
	return c.sendSetChannel(byte(channelIndex), name, psk)
}

func (c *MeshCoreClient) SetNodeName(name string) error {
	return c.sendSetAdvertName(name)
}

func (c *MeshCoreClient) SetNodeLocation(lat, lon float64) error {
	return c.sendSetAdvertLatLon(lat, lon)
}

func (c *MeshCoreClient) Reboot() error {
	return c.sendReboot()
}

func (c *MeshCoreClient) RequestStats() error {
	return c.sendGetStats(1) // statsTypeRadio
}

func (c *MeshCoreClient) RequestBattery() error {
	return c.sendGetBatteryAndStorage()
}

// ============================================================================
// Вспомогательные функции
// ============================================================================

func pubKeyHex(key []byte) string {
	return fmt.Sprintf("%x", key)
}

// ============================================================================
// Установка коллбэков
// ============================================================================

func (c *MeshCoreClient) OnContact(f func(*Contact)) {
	c.onContact = f
}
func (c *MeshCoreClient) OnChannel(f func(*Channel)) {
	c.onChannel = f
}
func (c *MeshCoreClient) OnMessage(f func(*Message)) {
	c.onMessage = f
}
func (c *MeshCoreClient) OnChannelMessage(f func(*ChannelMessage)) {
	c.onChannelMessage = f
}
func (c *MeshCoreClient) OnSelfInfo(f func(*SelfInfo)) {
	c.onSelfInfo = f
}
func (c *MeshCoreClient) OnStats(f func(*CompanionRadioStats)) {
	c.onStats = f
}
func (c *MeshCoreClient) OnBattery(f func(millivolts int, usedKb, totalKb int)) {
	c.onBattery = f
}
func (c *MeshCoreClient) OnError(f func(error)) {
	c.onError = f
}

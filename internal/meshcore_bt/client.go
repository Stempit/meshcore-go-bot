package meshcore_bt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log/slog"
	"strings"
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
	contacts    map[string]*Contact // ключ – hex(pubKey)
	channels    map[int]*Channel
	messages    map[string][]*Message // ключ – hex(senderKey)
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
	onNoMoreMessages func()

	// для ожидания подтверждения
	sentConfirmCh      chan struct{} // для respCodeSent
	deliveredConfirmCh chan struct{} // для pushCodeSendConfirmed
	confirmMu          sync.Mutex

	// канал входящих сообщений
	incomingCh chan interface{}
	wg         sync.WaitGroup

	// Логгер
	logger *slog.Logger
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
		incomingCh:  make(chan interface{}, 100),
		logger:      slog.Default(),
	}
}

// SetLogger позволяет установить собственный логгер.
func (c *MeshCoreClient) SetLogger(logger *slog.Logger) {
	c.logger = logger
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
	// Закрываем старый doneCh, если он ещё не закрыт (например, после разрыва)
	select {
	case <-c.doneCh:
		// уже закрыт
	default:
		close(c.doneCh)
	}
	c.doneCh = make(chan struct{})
	c.mu.Unlock()

	device, err := c.adapter.Connect(address, bluetooth.ConnectionParams{})
	if err != nil {
		c.logger.Error("Connect failed", "address", address.String(), "error", err)
		return fmt.Errorf("connect failed: %w", err)
	}
	c.mu.Lock()
	c.device = &device
	c.mu.Unlock()
	c.logger.Info("Connected to device", "address", address.String())

	services, err := device.DiscoverServices(nil)
	if err != nil {
		c.logger.Error("Discover services failed", "error", err)
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
		c.logger.Error("MeshCore service not found")
		device.Disconnect()
		return fmt.Errorf("meshcore service not found")
	}
	c.logger.Info("Found MeshCore service")

	chars, err := targetService.DiscoverCharacteristics(nil)
	if err != nil {
		c.logger.Error("Discover characteristics failed", "error", err)
		device.Disconnect()
		return fmt.Errorf("discover characteristics failed: %w", err)
	}
	for _, ch := range chars {
		uuid := ch.UUID().String()
		switch uuid {
		case rxCharacteristicUUID:
			c.rxChar = &ch
			c.logger.Info("Found RX characteristic")
		case txCharacteristicUUID:
			c.txChar = &ch
			c.logger.Info("Found TX characteristic")
		}
	}

	if c.rxChar == nil || c.txChar == nil {
		c.logger.Error("RX/TX characteristics not found")
		device.Disconnect()
		return fmt.Errorf("RX/TX characteristics not found")
	}

	c.logger.Info("Enabling notifications on TX characteristic...")
	if err := c.txChar.EnableNotifications(func(data []byte) {
		c.logger.Debug("Notification received", "data", fmt.Sprintf("%x", data))
		select {
		case c.notifyCh <- data:
		default:
			c.logger.Warn("Notification channel full, dropping data")
		}
	}); err != nil {
		c.logger.Error("EnableNotifications failed", "error", err)
		device.Disconnect()
		return fmt.Errorf("enable notifications failed: %w", err)
	}
	c.logger.Info("Notifications enabled")
	time.Sleep(500 * time.Millisecond)

	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()
	c.wg.Add(1) // для канала входящих сообщений
	go c.frameHandler()

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

	// // Повторная отправка AppStart todo: выпилить
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
	c.wg.Wait()         // ждём завершения frameHandler
	close(c.incomingCh) // закрываем канал сообщений
	if c.device != nil {
		c.logger.Info("Disconnecting from device")
		return c.device.Disconnect()
	}
	return nil
}

// ============================================================================
// Отправка команд (сборка фреймов)
// ============================================================================

func (c *MeshCoreClient) sendAppStart() error {
	// [cmd(1)][reserved(7)][app_name][0x00]
	// Firmware checks len >= 8, reads app_name from cmd_frame[8].
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdAppStart)
	buf.Write([]byte{0, 0, 0, 0, 0, 0, 0}) // 7 reserved bytes (bytes 1-7)
	buf.WriteString("MeshCoreOpen")
	buf.WriteByte(0)
	data := buf.Bytes()
	c.logger.Debug("Sending AppStart", "data", fmt.Sprintf("%x", data))
	return c.write(data)
}

func (c *MeshCoreClient) sendDeviceQuery() error {
	data := []byte{cmdDeviceQuery, appProtocolVersion}
	c.logger.Debug("Sending DeviceQuery", "data", fmt.Sprintf("%x", data))
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
	// имя: 32 байта, null-padded
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

func (c *MeshCoreClient) sendTextMessage(recipient []byte, text string, attempt byte, timestamp uint32) (string, error) {
	// [cmd(1)][txt_type(1)][attempt(1)][timestamp(4)][pub_key_prefix(6)][text][0x00]
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdSendTxtMsg)
	buf.WriteByte(txtTypePlain)
	buf.WriteByte(attempt)
	_ = binary.Write(buf, binary.LittleEndian, timestamp)
	if len(recipient) < 6 {
		return "", fmt.Errorf("recipient key too short")
	}
	buf.Write(recipient[:6])
	buf.WriteString(text)
	buf.WriteByte(0)

	// Генерируем ID сообщения так же, как в парсере
	messageID := fmt.Sprintf("%x-%d", recipient[:6], timestamp)

	if err := c.write(buf.Bytes()); err != nil {
		return messageID, err
	}
	return messageID, nil
}

// подтверждение отправки
func (c *MeshCoreClient) notifySent() {
	c.confirmMu.Lock()
	defer c.confirmMu.Unlock()
	if c.sentConfirmCh != nil {
		close(c.sentConfirmCh)
		c.sentConfirmCh = nil
	}
}

// подтверждение доставки
func (c *MeshCoreClient) notifyDelivered() {
	c.confirmMu.Lock()
	defer c.confirmMu.Unlock()
	if c.deliveredConfirmCh != nil {
		close(c.deliveredConfirmCh)
		c.deliveredConfirmCh = nil
	}
}

func (c *MeshCoreClient) sendChannelTextMessage(channelIndex byte, text string) error {
	// [cmd(1)][txt_type(1)][channel(1)][timestamp(4)][text][0x00]
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
	if len(pubKey) < 32 {
		return fmt.Errorf("reset path requires full 32-byte public key")
	}
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdResetPath)
	buf.Write(pubKey[:32])
	return c.write(buf.Bytes())
}

func (c *MeshCoreClient) sendUpdateContact(contact *Contact) error {
	if len(contact.PublicKey) < 32 {
		return fmt.Errorf("update contact requires full 32-byte public key")
	}
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdAddUpdateContact)
	buf.Write(contact.PublicKey[:32])
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
	// Таймстемп (last_advert_timestamp)
	_ = binary.Write(buf, binary.LittleEndian, uint32(time.Now().Unix()))
	// Координаты (если есть)
	latInt, lonInt := int32(0), int32(0)
	if contact.Latitude != nil && contact.Longitude != nil {
		latInt = int32(*contact.Latitude * 1e6)
		lonInt = int32(*contact.Longitude * 1e6)
	}
	_ = binary.Write(buf, binary.LittleEndian, latInt)
	_ = binary.Write(buf, binary.LittleEndian, lonInt)
	// last_modified (optional)
	_ = binary.Write(buf, binary.LittleEndian, uint32(0))
	return c.write(buf.Bytes())
}

func (c *MeshCoreClient) sendRemoveContact(pubKey []byte) error {
	if len(pubKey) < 32 {
		return fmt.Errorf("remove contact requires full 32-byte public key")
	}
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdRemoveContact)
	buf.Write(pubKey[:32])
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
	if err != nil {
		c.logger.Error("Write to characteristic failed", "error", err)
	}
	return err
}

// ============================================================================
// Обработчик входящих фреймов
// ============================================================================

func (c *MeshCoreClient) frameHandler() {
	defer c.wg.Done() // для канала входящих сообщений

	for {
		select {
		case data := <-c.notifyCh:
			c.logger.Debug("RX frame", "data", fmt.Sprintf("%x", data))
			c.handleFrame(data)
		case <-c.doneCh:
			return
		}
	}
}

// parseContactMessageV3 разбирает сообщение V3 от контакта (код 0x10).
//
// Формат (из MyMesh.cpp, queueMessage, app_target_ver >= 3):
//
//	[code(1)][snr(1)][res(1)][res(1)][prefix(6)][path_len(1)][txt_type(1)][timestamp(4)][extra(4, только для signed)][text]
//
// path_len — только метаданные из пакета; реальных байт пути в companion-фрейме нет.
// extra (4 байта sender_prefix) присутствует только когда txt_type == txtTypeSigned.
func (c *MeshCoreClient) parseContactMessageV3(data []byte) {
	// минимум: code(1)+snr+res+res(3)+prefix(6)+path_len(1)+txt_type(1)+timestamp(4) = 16
	const minLen = 1 + 3 + 6 + 1 + 1 + 4
	if len(data) < minLen {
		c.logger.Warn("parseContactMessageV3: data too short", "len", len(data))
		return
	}
	prefix := data[4:10]
	// data[10] = path_len (метаданные, байты пути в companion-фрейме не передаются)
	txtType := data[11]
	timestampRaw := binary.LittleEndian.Uint32(data[12:16])

	// Для signed-сообщений после timestamp идут 4 байта extra (sender_prefix)
	textStart := 16
	if txtType == txtTypeSigned {
		textStart = 20
	}
	if textStart >= len(data) {
		c.logger.Warn("parseContactMessageV3: text start beyond data", "textStart", textStart, "len", len(data))
		return
	}
	textBytes := data[textStart:]
	nullIdx := bytes.IndexByte(textBytes, 0)
	var text string
	if nullIdx >= 0 {
		text = string(textBytes[:nullIdx])
	} else {
		text = string(textBytes)
	}

	// Поиск контакта по префиксу
	var fullKey []byte
	var senderName string
	c.mu.RLock()
	for _, contact := range c.contacts {
		if len(contact.PublicKey) >= 6 && bytes.Equal(contact.PublicKey[:6], prefix) {
			fullKey = contact.PublicKey
			senderName = contact.Name
			break
		}
	}
	c.mu.RUnlock()

	if fullKey == nil {
		fullKey = prefix
		c.logger.Warn("parseContactMessageV3: unknown sender", "prefix", fmt.Sprintf("%x", prefix))
	}

	msg := &Message{
		SenderKey:  fullKey,
		Text:       text,
		Timestamp:  time.Unix(int64(timestampRaw), 0),
		IsOutgoing: false,
		Status:     "delivered",
		MessageID:  fmt.Sprintf("%x-%d", prefix, timestampRaw),
	}

	c.mu.Lock()
	key := pubKeyHex(fullKey)
	c.messages[key] = append(c.messages[key], msg)
	c.mu.Unlock()

	c.logger.Info("Received contact message V3", "sender", senderName, "key", key, "text", text)

	if c.onMessage != nil {
		c.onMessage(msg)
	}

	select {
	case c.incomingCh <- msg:
	default:
		c.logger.Warn("Incoming message channel full, dropping message")
	}
}

// parseChannelMessageV3 разбирает сообщение V3 в канале (код 0x11).
//
// Формат (из MyMesh.cpp, onChannelMessageRecv, app_target_ver >= 3):
//
//	[code(1)][snr(1)][res(1)][res(1)][channel(1)][path_len(1)][txt_type(1)][timestamp(4)][text]
//
// Префикс отправителя в канальных сообщениях НЕ передаётся.
// Имя отправителя встроено в текст в формате "Имя: текст".
func (c *MeshCoreClient) parseChannelMessageV3(data []byte) {
	// минимум: code(1)+snr+res+res(3)+channel(1)+path_len(1)+txt_type(1)+timestamp(4) = 11
	const minLen = 1 + 3 + 1 + 1 + 1 + 4
	if len(data) < minLen {
		c.logger.Warn("parseChannelMessageV3: data too short", "len", len(data))
		return
	}
	channelIndex := int(data[4])
	// data[5] = path_len (метаданные, байты пути не передаются)
	// data[6] = txt_type
	timestampRaw := binary.LittleEndian.Uint32(data[7:11])

	textStart := 11
	if textStart >= len(data) {
		c.logger.Warn("parseChannelMessageV3: text start beyond data", "textStart", textStart, "len", len(data))
		return
	}
	textBytes := data[textStart:]
	nullIdx := bytes.IndexByte(textBytes, 0)
	var text string
	if nullIdx >= 0 {
		text = string(textBytes[:nullIdx])
	} else {
		text = string(textBytes)
	}

	// Поиск имени отправителя по первым байтам текста «Имя: »,
	// которые прошивка встраивает в sendGroupMessage.
	senderName := ""
	if idx := strings.Index(text, ": "); idx > 0 {
		senderName = text[:idx]
		text = text[idx+2:]
		c.logger.Debug("Extracted sender name from channel text V3", "name", senderName, "text", text)
	} else {
		senderName = fmt.Sprintf("?ch%d", channelIndex)
		c.logger.Warn("parseChannelMessageV3: no sender prefix in text")
	}

	msg := &ChannelMessage{
		ChannelIndex: channelIndex,
		SenderName:   senderName,
		Text:         text,
		Timestamp:    time.Unix(int64(timestampRaw), 0),
		IsOutgoing:   false,
		Status:       "delivered",
		MessageID:    fmt.Sprintf("%d-%d", channelIndex, timestampRaw),
	}

	c.mu.Lock()
	c.channelMsgs[channelIndex] = append(c.channelMsgs[channelIndex], msg)
	c.mu.Unlock()

	c.logger.Info("Channel message V3 received", "channel", channelIndex, "sender", senderName, "text", text)

	if c.onChannelMessage != nil {
		c.onChannelMessage(msg)
	}

	// отправляем в канал входящих сообщений
	select {
	case c.incomingCh <- msg:
	default:
		c.logger.Warn("Incoming message channel full, dropping message")
	}
}

// parseContactMessage разбирает сообщение от контакта (код 0x07).
//
// Формат (из MyMesh.cpp, queueMessage, app_target_ver < 3):
//
//	[code(1)][prefix(6)][path_len(1)][txt_type(1)][timestamp(4)][extra(4, только для signed)][text]
//
// path_len — метаданные из пакета (например, 0xFF = прямая доставка).
// Реальных байт пути в companion-фрейме нет.
func (c *MeshCoreClient) parseContactMessage(data []byte) {
	// минимум: code(1)+prefix(6)+path_len(1)+txt_type(1)+timestamp(4) = 13
	const minLen = 1 + 6 + 1 + 1 + 4
	if len(data) < minLen {
		c.logger.Warn("parseContactMessage: data too short", "len", len(data))
		return
	}
	prefix := data[1:7]
	// data[7] = path_len (метаданные, без реальных байт пути)
	txtType := data[8]
	timestampRaw := binary.LittleEndian.Uint32(data[9:13])

	// Для signed-сообщений после timestamp идут 4 байта extra (sender_prefix)
	textStart := 13
	if txtType == txtTypeSigned {
		textStart = 17
	}
	if textStart >= len(data) {
		c.logger.Warn("parseContactMessage: no text data")
		return
	}
	textBytes := data[textStart:]
	nullIdx := bytes.IndexByte(textBytes, 0)
	var text string
	if nullIdx >= 0 {
		text = string(textBytes[:nullIdx])
	} else {
		text = string(textBytes)
	}

	var fullKey []byte
	c.mu.RLock()
	for _, contact := range c.contacts {
		if len(contact.PublicKey) >= 6 && bytes.Equal(contact.PublicKey[:6], prefix) {
			fullKey = contact.PublicKey
			break
		}
	}
	c.mu.RUnlock()
	if fullKey == nil {
		fullKey = prefix
		c.logger.Warn("parseContactMessage: unknown sender", "prefix", fmt.Sprintf("%x", prefix))
	}

	msg := &Message{
		SenderKey:  fullKey,
		Text:       text,
		Timestamp:  time.Unix(int64(timestampRaw), 0),
		IsOutgoing: false,
		Status:     "delivered",
		MessageID:  fmt.Sprintf("%x-%d", prefix, timestampRaw),
	}

	c.mu.Lock()
	key := pubKeyHex(fullKey)
	c.messages[key] = append(c.messages[key], msg)
	c.mu.Unlock()

	c.logger.Debug("Received contact message", "sender", key, "text", text)

	if c.onMessage != nil {
		c.onMessage(msg)
	}

	// отправляем в канал входящих сообщений
	select {
	case c.incomingCh <- msg:
	default:
		c.logger.Warn("Incoming message channel full, dropping message")
	}
}

// parseChannelMessage разбирает сообщение в канале (код 0x08).
//
// Формат (из MyMesh.cpp, onChannelMessageRecv, app_target_ver < 3):
//
//	[code(1)][channel(1)][path_len(1)][txt_type(1)][timestamp(4)][text]
//
// Префикс отправителя в канальных сообщениях НЕ передаётся.
// Имя отправителя встроено в текст в формате "Имя: текст".
func (c *MeshCoreClient) parseChannelMessage(data []byte) {
	// минимум: code(1)+channel(1)+path_len(1)+txt_type(1)+timestamp(4) = 8
	const minLen = 1 + 1 + 1 + 1 + 4
	if len(data) < minLen {
		c.logger.Warn("parseChannelMessage: data too short", "len", len(data))
		return
	}
	channelIndex := int(data[1])
	// data[2] = path_len (метаданные)
	// data[3] = txt_type (для каналов всегда TXT_TYPE_PLAIN)
	timestampRaw := binary.LittleEndian.Uint32(data[4:8])

	textStart := 8
	if textStart >= len(data) {
		c.logger.Warn("parseChannelMessage: no text data")
		return
	}
	textBytes := data[textStart:]
	nullIdx := bytes.IndexByte(textBytes, 0)
	var text string
	if nullIdx >= 0 {
		text = string(textBytes[:nullIdx])
	} else {
		text = string(textBytes)
	}

	// Имя отправителя встроено прошивкой в текст: «Имя: сообщение»
	senderName := ""
	if idx := strings.Index(text, ": "); idx > 0 {
		senderName = text[:idx]
		text = text[idx+2:]
		c.logger.Debug("Extracted sender name from channel text", "name", senderName, "text", text)
	} else {
		senderName = fmt.Sprintf("?ch%d", channelIndex)
		c.logger.Warn("parseChannelMessage: no sender prefix in text")
	}

	msg := &ChannelMessage{
		ChannelIndex: channelIndex,
		SenderName:   senderName,
		Text:         text,
		Timestamp:    time.Unix(int64(timestampRaw), 0),
		IsOutgoing:   false,
		Status:       "delivered",
		MessageID:    fmt.Sprintf("%d-%d", channelIndex, timestampRaw),
	}

	c.mu.Lock()
	c.channelMsgs[channelIndex] = append(c.channelMsgs[channelIndex], msg)
	c.mu.Unlock()

	c.logger.Debug("Channel message received", "channel", channelIndex, "sender", senderName, "text", text)

	if c.onChannelMessage != nil {
		c.onChannelMessage(msg)
	}

	// отправляем в канал входящих сообщений
	select {
	case c.incomingCh <- msg:
	default:
		c.logger.Warn("Incoming message channel full, dropping message")
	}
}

func (c *MeshCoreClient) handleFrame(data []byte) {
	if len(data) == 0 {
		return
	}
	code := data[0]
	c.logger.Debug("handleFrame", "code", fmt.Sprintf("0x%02x", code), "len", len(data))
	switch code {
	case respCodeSelfInfo:
		c.parseSelfInfo(data)
	case respCodeDeviceInfo:
		c.parseDeviceInfo(data)
	case respCodeContactsStart:
		// Начало списка контактов
	case respCodeContact:
		if len(data) < contactFrameSize {
			c.parseContactV3(data)
		} else {
			c.parseContact(data)
		}
	case respCodeEndOfContacts:
		// Конец списка
	case respCodeChannelInfo:
		if len(data) < 2 {
			c.logger.Warn("ChannelInfo too short", "len", len(data))
			return
		}
		c.parseChannelInfo(data)
	case respCodeContactMsgRecv:
		c.parseContactMessage(data)
	case respCodeContactMsgRecvV3:
		c.parseContactMessageV3(data)
	case respCodeChannelMsgRecv:
		c.parseChannelMessage(data)
	case respCodeChannelMsgRecvV3:
		c.parseChannelMessageV3(data)
	case respCodeSent:
		c.parseSent(data)
	case respCodeNoMoreMessages:
		c.logger.Debug("Received respCodeNoMoreMessages")
		if c.onNoMoreMessages != nil {
			c.onNoMoreMessages()
		}
	case pushCodeSendConfirmed:
		c.parseSendConfirmed(data)
	case pushCodePathUpdated:
		c.parsePathUpdated(data)
	case pushCodeMsgWaiting:
		// Есть сообщения в очереди, запросить
		c.logger.Debug("Messages waiting, requesting sync")
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
		c.parseOk(data)
	case respCodeErr:
		c.logger.Error("Received error response", "data", fmt.Sprintf("%x", data))
	case pushCodeAdvert:
		c.parseAdvert(data)
	case pushCodeLogRxData:
		c.parseLogRxData(data)
	default:
		c.logger.Warn("Unknown frame code", "code", code)
	}
}

// ============================================================================
// Парсеры ответов
// ============================================================================

// parseSelfInfo разбирает RESP_CODE_SELF_INFO (0x05).
//
// Формат (из MyMesh.cpp, handleCmdFrame, CMD_APP_START):
//
//	[code(1)][adv_type(1)][tx_power(1)][max_tx_power(1)][pub_key(32)]
//	[lat(4)][lon(4)][multi_acks(1)][adv_loc_policy(1)][tele_flags(1)]
//	[manual_add(1)][freq_hz(4)][bw_hz(4)][sf(1)][cr(1)][name...]
func (c *MeshCoreClient) parseSelfInfo(data []byte) {
	c.logger.Debug("parseSelfInfo", "bytes", len(data))
	// минимум до имени: 1+1+1+1+32+4+4+1+1+1+1+4+4+1+1 = 58 байт
	if len(data) < 58 {
		c.logger.Warn("parseSelfInfo: data too short", "len", len(data))
		return
	}
	info := &SelfInfo{}
	r := bytes.NewReader(data)
	_, _ = r.Seek(2, 0) // пропускаем code + adv_type

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

	// manual_add_contacts: 0 = auto-add enabled (ManualAdd = false),
	// non-zero = manual add required (ManualAdd = true).
	var manualAdd byte
	_ = binary.Read(r, binary.LittleEndian, &manualAdd)
	info.ManualAdd = manualAdd != 0

	// Частота передаётся как freq*1000 (Гц*1000 = кГц)
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

	// Имя — до конца фрейма, null-terminated (или без нуля)
	nameBytes := make([]byte, 0)
	for {
		b, err := r.ReadByte()
		if err != nil || b == 0 {
			break
		}
		nameBytes = append(nameBytes, b)
	}
	info.Name = string(nameBytes)

	// client_repeat (firmware v9+) — 1 байт после имени, если есть
	if r.Len() >= 1 {
		var clientRepeat byte
		_ = binary.Read(r, binary.LittleEndian, &clientRepeat)
		info.ClientRepeat = clientRepeat != 0
	}

	c.mu.Lock()
	c.selfInfo = info
	c.mu.Unlock()
	c.logger.Debug("SelfInfo updated", "name", info.Name, "public_key", fmt.Sprintf("%x", info.PublicKey))
	if c.onSelfInfo != nil {
		c.onSelfInfo(info)
	}
}

// parseDeviceInfo разбирает RESP_CODE_DEVICE_INFO (0x0D).
//
// Формат (из MyMesh.cpp, CMD_DEVICE_QEURY):
//
//	[code(1)][fw_ver(1)][max_contacts_raw(1)][max_channels(1)][ble_pin(4)]
//	[fw_build(12)][model(40)][version(20)][client_repeat(1, v9+)][path_hash_mode(1, v10+)]
func (c *MeshCoreClient) parseDeviceInfo(data []byte) {
	if len(data) < 2 {
		c.logger.Warn("DeviceInfo too short", "len", len(data))
		return
	}
	c.logger.Debug("DeviceInfo received", "data", fmt.Sprintf("%x", data))
	fwVer := data[1]
	c.logger.Info("Device info", "firmware_version", fwVer)

	if fwVer >= 3 && len(data) >= 80 {
		maxContacts := int(data[2]) * 2
		maxChannels := int(data[3])
		blePin := binary.LittleEndian.Uint32(data[4:8])
		fwBuild := string(bytes.TrimRight(data[8:20], "\x00"))
		model := string(bytes.TrimRight(data[20:60], "\x00"))
		version := string(bytes.TrimRight(data[60:80], "\x00"))

		c.logger.Info("Device details",
			"max_contacts", maxContacts,
			"max_channels", maxChannels,
			"ble_pin", blePin,
			"firmware_build", fwBuild,
			"model", model,
			"version", version)

		if len(data) > 81 {
			c.logger.Info("Additional device info",
				"client_repeat_enabled", data[80] != 0,
				"path_hash_mode", int(data[81]))
		}
	}
}

// parseContact разбирает полный RESP_CODE_CONTACT (0x03).
//
// Формат (из MyMesh.cpp, writeContactRespFrame):
//
//	[code(1)][pub_key(32)][type(1)][flags(1)][path_len(1)][path(64)]
//	[name(32)][last_advert_timestamp(4)][gps_lat(4)][gps_lon(4)][lastmod(4)]
//
// Итого: 1+32+1+1+1+64+32+4+4+4+4 = 148 байт (contactFrameSize).
func (c *MeshCoreClient) parseContact(data []byte) {
	const minLen = 1 + 32 + 1 + 1 + 1 + 64 + 32 + 4 + 4 + 4
	if len(data) < minLen {
		c.parseContactV3(data)
		return
	}
	pubKey := data[1:33]
	contactType := int(data[33])
	flags := int(data[34])
	pathLen := int(data[35])
	// Ограничиваем длину пути максимально допустимой (64)
	if pathLen > 64 {
		pathLen = 64
	}
	// путь всегда занимает ровно 64 байта в фрейме
	path := data[36:100]
	actualPath := path[:pathLen]

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
	// data[144:148] = lastmod (игнорируется)

	contact := &Contact{
		PublicKey:  pubKey,
		Name:       name,
		Type:       contactType,
		Flags:      flags,
		PathLength: pathLen,
		Path:       actualPath,
		Latitude:   lat,
		Longitude:  lon,
		LastSeen:   lastSeen,
	}

	c.updateMessagesWithFullKey(contact.PublicKey)

	c.mu.Lock()
	c.contacts[pubKeyHex(pubKey)] = contact
	c.mu.Unlock()
	c.logger.Debug("Contact parsed", "name", name, "public_key", fmt.Sprintf("%x", pubKey))
	if c.onContact != nil {
		c.onContact(contact)
	}
}

func (c *MeshCoreClient) parseContactV3(data []byte) {
	// Минимальная длина: code(1) + 6-байтный префикс + хотя бы 1 байт имени + нуль
	if len(data) < 1+6+2 {
		c.logger.Warn("ContactV3 too short", "len", len(data))
		return
	}
	prefix := data[1:7]
	nameStart := 7
	nameBytes := data[nameStart:]
	nullIdx := bytes.IndexByte(nameBytes, 0)
	var name string
	if nullIdx >= 0 {
		name = string(nameBytes[:nullIdx])
	} else {
		name = string(nameBytes)
	}

	// Пытаемся найти полный ключ по префиксу
	var fullKey []byte
	c.mu.RLock()
	for _, contact := range c.contacts {
		if len(contact.PublicKey) >= 6 && bytes.Equal(contact.PublicKey[:6], prefix) {
			fullKey = contact.PublicKey
			break
		}
	}
	c.mu.RUnlock()
	if fullKey == nil {
		fullKey = prefix
		c.logger.Warn("ContactV3: unknown prefix, using as key", "prefix", fmt.Sprintf("%x", prefix))
	}

	contact := &Contact{
		PublicKey:  fullKey,
		Name:       name,
		Type:       0,
		Flags:      0,
		PathLength: 0,
		Path:       []byte{},
		Latitude:   nil,
		Longitude:  nil,
		LastSeen:   time.Now(),
	}

	c.mu.Lock()
	c.contacts[pubKeyHex(fullKey)] = contact
	c.mu.Unlock()
	c.logger.Debug("ContactV3 parsed", "name", name, "public_key", fmt.Sprintf("%x", fullKey))
	if c.onContact != nil {
		c.onContact(contact)
	}
}

// parseChannelInfo разбирает RESP_CODE_CHANNEL_INFO (0x12).
//
// Формат (из MyMesh.cpp, CMD_GET_CHANNEL):
//
//	[code(1)][channel_idx(1)][name(32, null-padded)][psk(16)]
//
// Имя занимает ровно 32 байта (фиксировано), PSK — ровно 16 байт после имени.
func (c *MeshCoreClient) parseChannelInfo(data []byte) {
	// минимум: code(1)+idx(1)+name(32)+psk(16) = 50 байт
	const minLen = 1 + 1 + 32 + 16
	if len(data) < minLen {
		c.logger.Warn("ChannelInfo too short", "len", len(data))
		return
	}
	index := int(data[1])
	// Имя занимает фиксированные 32 байта в позиции 2..33.
	// Firmware пишет имя через strcpy(), поэтому после нуль-терминатора
	// могут идти произвольные байты из буфера — берём только до первого нуля.
	nameRaw := data[2:34]
	nameEnd := bytes.IndexByte(nameRaw, 0)
	var name string
	if nameEnd >= 0 {
		name = string(nameRaw[:nameEnd])
	} else {
		name = string(nameRaw)
	}
	// PSK — 16 байт после фиксированного имени, в позиции 34..49
	psk := make([]byte, 16)
	copy(psk, data[34:50])

	ch := &Channel{
		Index:   index,
		Name:    name,
		PSK:     psk,
		IsEmpty: len(name) == 0,
	}
	c.mu.Lock()
	c.channels[index] = ch
	c.mu.Unlock()
	c.logger.Debug("Channel info parsed", "index", index, "name", name)
	if c.onChannel != nil {
		c.onChannel(ch)
	}
}

func (c *MeshCoreClient) parseSent(data []byte) {
	c.logger.Debug("Sent confirmation received")
	c.notifySent()
}

func (c *MeshCoreClient) parseSendConfirmed(data []byte) {
	c.logger.Debug("Send confirmed")
	c.notifyDelivered()
}

func (c *MeshCoreClient) parsePathUpdated(data []byte) {
	c.logger.Debug("Path updated", "data", fmt.Sprintf("%x", data))
}

func (c *MeshCoreClient) parseAdvert(data []byte) {
	// Пока просто логируем
	c.logger.Info("Advert received", "data", fmt.Sprintf("%x", data))
	// Здесь можно разобрать пакет и уведомить коллбэк, если нужно
}

func (c *MeshCoreClient) parseOk(data []byte) {
	c.logger.Debug("Received OK")
	c.notifySent() // для канальных сообщений OK означает принятие команды
}

func (c *MeshCoreClient) parseLogRxData(data []byte) {
	c.logger.Debug("Log RX data received", "data", fmt.Sprintf("%x", data))
	// Можно игнорировать или использовать для отладки
}

// parseBatteryAndStorage разбирает RESP_CODE_BATT_AND_STORAGE (0x0C).
//
// Формат (из MyMesh.cpp, CMD_GET_BATT_AND_STORAGE):
//
//	[code(1)][battery_mv(2)][used_kb(4)][total_kb(4)]
func (c *MeshCoreClient) parseBatteryAndStorage(data []byte) {
	if len(data) < 11 {
		c.logger.Warn("BatteryAndStorage too short", "len", len(data))
		return
	}
	mv := binary.LittleEndian.Uint16(data[1:3])
	usedKb := binary.LittleEndian.Uint32(data[3:7])
	totalKb := binary.LittleEndian.Uint32(data[7:11])
	c.logger.Info("Battery and storage", "millivolts", mv, "used_kb", usedKb, "total_kb", totalKb)
	if c.onBattery != nil {
		c.onBattery(int(mv), int(usedKb), int(totalKb))
	}
}

// parseStats разбирает RESP_CODE_STATS (0x18).
//
// Формат для STATS_TYPE_RADIO (из MyMesh.cpp, CMD_GET_STATS, stats_type==1):
//
//	[code(1)][stats_type(1)][noise_floor(2, int16)][last_rssi(1, int8)]
//	[last_snr(1, int8 x4)][tx_air_secs(4)][rx_air_secs(4)]
func (c *MeshCoreClient) parseStats(data []byte) {
	if len(data) < 2 {
		c.logger.Warn("Stats too short", "len", len(data))
		return
	}
	statsType := data[1]
	stats := &CompanionRadioStats{}

	switch statsType {
	case 1: // STATS_TYPE_RADIO
		// [noise_floor(2)][last_rssi(1)][last_snr(1)][tx_air_secs(4)][rx_air_secs(4)]
		if len(data) < 14 {
			c.logger.Warn("Stats RADIO too short", "len", len(data))
			return
		}
		noiseFloor := int16(binary.LittleEndian.Uint16(data[2:4]))
		stats.NoiseFloorDbm = int8(noiseFloor) // сохраняем как int8 (усечение)
		// data[4] = last_rssi (не используется в модели)
		stats.LastSnrDb = float32(int8(data[5])) / 4.0
		stats.TxAirSecs = binary.LittleEndian.Uint32(data[6:10])
		stats.RxAirSecs = binary.LittleEndian.Uint32(data[10:14])
	default:
		c.logger.Debug("Stats: unsupported stats_type, skipping", "type", statsType)
		return
	}

	c.logger.Debug("Stats received", "stats", fmt.Sprintf("%+v", stats))
	if c.onStats != nil {
		c.onStats(stats)
	}
}

func (c *MeshCoreClient) parseAutoAddConfig(data []byte) {
	c.logger.Debug("AutoAddConfig received")
}

func (c *MeshCoreClient) parseCustomVars(data []byte) {
	c.logger.Debug("CustomVars received")
}

// updateMessagesWithFullKey заменяет 6-байтные префиксные ключи в сообщениях
// на полный 32-байтный ключ, как только он становится известен из контакта.
func (c *MeshCoreClient) updateMessagesWithFullKey(fullKey []byte) {
	if len(fullKey) < 6 {
		return
	}
	prefix := fullKey[:6]
	prefixStr := pubKeyHex(prefix)
	c.mu.Lock()
	defer c.mu.Unlock()
	// Ищем сообщения, у которых senderKey == prefix
	if messages, ok := c.messages[prefixStr]; ok {
		// Перекладываем в новый ключ
		fullKeyStr := pubKeyHex(fullKey)
		c.messages[fullKeyStr] = append(c.messages[fullKeyStr], messages...)
		delete(c.messages, prefixStr)
		// Обновляем SenderKey в каждом сообщении
		for _, msg := range c.messages[fullKeyStr] {
			msg.SenderKey = fullKey
		}
	}
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

// SendMessageWithRetry отправляет личное сообщение с повторными попытками.
// waitForDelivery: true — ждать pushCodeSendConfirmed, false — ждать respCodeSent.
// maxRetries: число повторов (0 = одна попытка).
// timeout: таймаут ожидания подтверждения для каждой попытки.
func (c *MeshCoreClient) SendMessageWithRetry(contact *Contact, text string, waitForDelivery bool, maxRetries int, timeout time.Duration) error {
	defer time.Sleep(150 * time.Microsecond) // частая отправка приводит к пропаже сообщений

	c.logger.Info("Sending private message with retry",
		"key", fmt.Sprintf("%x", contact.PublicKey), "name", contact.Name,
		"text", text, "waitForDelivery", waitForDelivery)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		timestamp := uint32(time.Now().Unix())
		_, err := c.sendTextMessage(contact.PublicKey, text, byte(attempt), timestamp)
		if err != nil {
			if attempt == maxRetries {
				return fmt.Errorf("send failed after %d attempts: %w", maxRetries+1, err)
			}
			time.Sleep(timeout)
			continue
		}

		// Создаём канал для ожидания
		confirmCh := make(chan struct{})
		c.confirmMu.Lock()
		if waitForDelivery {
			c.deliveredConfirmCh = confirmCh
		} else {
			c.sentConfirmCh = confirmCh
		}
		c.confirmMu.Unlock()

		select {
		case <-confirmCh:
			return nil
		case <-time.After(timeout):
			c.confirmMu.Lock()
			if waitForDelivery {
				if c.deliveredConfirmCh == confirmCh {
					c.deliveredConfirmCh = nil
				}
			} else {
				if c.sentConfirmCh == confirmCh {
					c.sentConfirmCh = nil
				}
			}
			c.confirmMu.Unlock()
			if attempt == maxRetries {
				return fmt.Errorf("no %s confirmation after %d attempts",
					map[bool]string{true: "delivery", false: "sent"}[waitForDelivery], maxRetries+1)
			}
		}
	}
	return nil
}

// SendMessage отправляет личное сообщение (без ожидания подтверждения).
func (c *MeshCoreClient) SendMessage(contact *Contact, text string) error {
	timestamp := uint32(time.Now().Unix())
	_, err := c.sendTextMessage(contact.PublicKey, text, 0, timestamp)
	return err
}

// SendChannelMessageWithRetry отправляет сообщение в канал с повторными попытками.
// waitForDelivery: true – ждать pushCodeSendConfirmed (доставка), false – ждать respCodeSent (отправка в эфир)
// maxRetries: максимальное число повторных попыток (0 – одна попытка без повторов)
// timeout: таймаут ожидания подтверждения после каждой отправки
func (c *MeshCoreClient) SendChannelMessageWithRetry(channel *Channel, text string, waitForDelivery bool, maxRetries int, timeout time.Duration) error {
	defer time.Sleep(150 * time.Microsecond) // частая отправка приводит к пропаже сообщений

	c.logger.Info("Sending channel message with retry",
		"channel", channel.Index, "name", channel.Name,
		"text", text, "waitForDelivery", waitForDelivery)

	if waitForDelivery {
		c.logger.Warn("waitForDelivery unavailable for channel messages, forcing false")
		waitForDelivery = false
	}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Отправляем сообщение (без генерации ID, так как протокол его не возвращает)
		if err := c.sendChannelTextMessage(byte(channel.Index), text); err != nil {
			if attempt == maxRetries {
				return fmt.Errorf("send failed after %d attempts: %w", maxRetries+1, err)
			}
			time.Sleep(timeout)
			continue
		}

		// Создаём канал для ожидания нужного подтверждения
		confirmCh := make(chan struct{})
		c.confirmMu.Lock()
		c.sentConfirmCh = confirmCh
		c.confirmMu.Unlock()

		select {
		case <-confirmCh:
			return nil
		case <-time.After(timeout):
			c.confirmMu.Lock()
			if c.sentConfirmCh == confirmCh {
				c.sentConfirmCh = nil
			}
			c.confirmMu.Unlock()
			if attempt == maxRetries {
				return fmt.Errorf("no sent confirmation after %d attempts", maxRetries+1)
			}
		}
	}
	return nil
}

// SendChannelMessage отправляет сообщение в канал (без ожидания подтверждения).
func (c *MeshCoreClient) SendChannelMessage(channel *Channel, text string) error {
	return c.sendChannelTextMessage(byte(channel.Index), text) //todo: обратную совместимость
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
	return c.sendGetStats(1) // STATS_TYPE_RADIO
}

func (c *MeshCoreClient) RequestBattery() error {
	return c.sendGetBatteryAndStorage()
}

// SyncNextMessage запрашивает следующее сообщение из очереди устройства.
func (c *MeshCoreClient) SyncNextMessage() error {
	return c.sendSyncNextMessage()
}

// OnNoMoreMessages устанавливает коллбэк для respCodeNoMoreMessages.
func (c *MeshCoreClient) OnNoMoreMessages(f func()) {
	c.onNoMoreMessages = f
}

func (c *MeshCoreClient) GetAllMessages() []*Message {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var all []*Message
	for _, msgs := range c.messages {
		all = append(all, msgs...)
	}
	return all
}

func (c *MeshCoreClient) GetAllChannelMessages() []*ChannelMessage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var all []*ChannelMessage
	for _, msgs := range c.channelMsgs {
		all = append(all, msgs...)
	}
	return all
}

// IncomingMessages возвращает канал для чтения входящих сообщений (*Message и *ChannelMessage).
func (c *MeshCoreClient) IncomingMessages() <-chan interface{} {
	return c.incomingCh
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

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
	// [cmd][app_ver][reserved 6][app_name]\0
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdAppStart)
	buf.WriteByte(1)
	buf.Write([]byte{0, 0, 0, 0, 0, 0})
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

func (c *MeshCoreClient) sendTextMessage(recipient []byte, text string, attempt byte, timestamp uint32) (string, error) {
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

// parseContactMessageV3 разбирает сообщение V3 от контакта (код 0x10)
func (c *MeshCoreClient) parseContactMessageV3(data []byte) {
	// формат: [code][snr][res][res][prefix(6)][path_len][txt_type][timestamp(4)][path(path_len)][text\0]
	const minLen = 1 + 3 + 6 + 1 + 1 + 4 // 16
	if len(data) < minLen {
		c.logger.Warn("parseContactMessageV3: data too short", "len", len(data))
		return
	}
	prefix := data[4:10]
	pathLen := int(data[10])
	_ = data[11] // txtType
	timestampRaw := binary.LittleEndian.Uint32(data[12:16])

	textStart := 16 + pathLen
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

// parseChannelMessageV3 разбирает сообщение V3 в канале (код 0x11)
func (c *MeshCoreClient) parseChannelMessageV3(data []byte) {
	// формат: [code][snr][res][res][channel][prefix(6)][path_len][txt_type][timestamp(4)][path(path_len)][text\0]
	const minLen = 1 + 3 + 1 + 6 + 1 + 1 + 4 // 17
	if len(data) < minLen {
		c.logger.Warn("parseChannelMessageV3: data too short", "len", len(data))
		return
	}
	channelIndex := int(data[4])
	prefix := data[5:11]
	pathLen := int(data[11]) // длина пути
	_ = data[12]             // txtType (пока не используется)
	timestampRaw := binary.LittleEndian.Uint32(data[13:17])

	// Пропускаем путь, если он есть
	textStart := 17 + pathLen
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

	// Поиск имени отправителя
	senderName := ""
	c.mu.RLock()
	for _, contact := range c.contacts {
		if len(contact.PublicKey) >= 6 && bytes.Equal(contact.PublicKey[:6], prefix) {
			senderName = contact.Name
			break
		}
	}
	c.mu.RUnlock()

	if senderName == "" {
		// Извлекаем имя из текста: ищем ": "
		if idx := strings.Index(text, ": "); idx > 0 {
			senderName = text[:idx]
			text = text[idx+2:]
			c.logger.Debug("Extracted sender name from text", "name", senderName, "cleanText", text)
		} else {
			senderName = fmt.Sprintf("?%x", prefix)
			c.logger.Warn("ChannelMessageV3: unknown sender", "prefix", fmt.Sprintf("%x", prefix))
		}
	}

	msg := &ChannelMessage{
		ChannelIndex: channelIndex,
		SenderName:   senderName,
		Text:         text,
		Timestamp:    time.Unix(int64(timestampRaw), 0),
		IsOutgoing:   false,
		Status:       "delivered",
		MessageID:    fmt.Sprintf("%d-%d-%x", channelIndex, timestampRaw, prefix),
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

// parseContactMessage разбирает старое сообщение от контакта (код 0x07)
func (c *MeshCoreClient) parseContactMessage(data []byte) {
	// формат: [code][prefix(6)][timestamp(4)][path_len(1)][path(path_len)][text\0]
	if len(data) < 1+6+4+1+1 {
		c.logger.Warn("parseContactMessage: data too short", "len", len(data))
		return
	}
	prefix := data[1:7]
	timestampRaw := binary.LittleEndian.Uint32(data[7:11])
	pathLen := int(data[11])
	pathStart := 12
	if pathStart+pathLen > len(data) {
		c.logger.Warn("parseContactMessage: invalid path length", "pathLen", pathLen, "dataLen", len(data))
		return
	}
	path := data[pathStart : pathStart+pathLen]
	textStart := pathStart + pathLen
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
		PathLength: pathLen,
		PathBytes:  path,
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

// parseChannelMessage разбирает старое сообщение в канале (код 0x08)
func (c *MeshCoreClient) parseChannelMessage(data []byte) {
	// формат: [code][channel][prefix(6)][timestamp(4)][path_len(1)][path(path_len)][text\0]
	if len(data) < 1+1+6+4+1+1 {
		c.logger.Warn("parseChannelMessage: data too short", "len", len(data))
		return
	}
	channelIndex := int(data[1])
	prefix := data[2:8]
	timestampRaw := binary.LittleEndian.Uint32(data[8:12])
	pathLen := int(data[12])
	pathStart := 13
	if pathStart+pathLen > len(data) {
		c.logger.Warn("parseChannelMessage: invalid path length", "pathLen", pathLen, "dataLen", len(data))
		return
	}
	path := data[pathStart : pathStart+pathLen]
	textStart := pathStart + pathLen
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

	// Поиск имени отправителя по префиксу
	senderName := ""
	c.mu.RLock()
	for _, contact := range c.contacts {
		if len(contact.PublicKey) >= 6 && bytes.Equal(contact.PublicKey[:6], prefix) {
			senderName = contact.Name
			break
		}
	}
	c.mu.RUnlock()

	if senderName == "" {
		// Извлекаем имя из текста: ищем ": " в начале
		if idx := strings.Index(text, ": "); idx > 0 {
			senderName = text[:idx]
			text = text[idx+2:] // удаляем "Имя: " из текста
			c.logger.Debug("Extracted sender name from text", "name", senderName, "cleanText", text)
		} else {
			senderName = fmt.Sprintf("?%x", prefix)
			c.logger.Warn("ChannelMessageV3: unknown sender", "prefix", fmt.Sprintf("%x", prefix))
		}
	}

	msg := &ChannelMessage{
		ChannelIndex: channelIndex,
		SenderName:   senderName,
		Text:         text,
		Timestamp:    time.Unix(int64(timestampRaw), 0),
		IsOutgoing:   false,
		Status:       "delivered",
		PathLength:   pathLen,
		PathBytes:    path,
		MessageID:    fmt.Sprintf("%d-%d-%x", channelIndex, timestampRaw, prefix),
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
		// Можно добавить проверку на сокращённый формат канала, если нужно
		c.parseChannelInfo(data)
	case respCodeContactMsgRecv, respCodeContactMsgRecvV3:
		if code == respCodeContactMsgRecvV3 {
			c.parseContactMessageV3(data)
		} else {
			c.parseContactMessage(data)
		}
	case respCodeChannelMsgRecv, respCodeChannelMsgRecvV3:
		if code == respCodeChannelMsgRecvV3 {
			c.parseChannelMessageV3(data)
		} else {
			c.parseChannelMessage(data)
		}
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

func (c *MeshCoreClient) parseSelfInfo(data []byte) {
	c.logger.Debug("parseSelfInfo", "bytes", len(data))
	if len(data) < 59 {
		c.logger.Warn("parseSelfInfo: data too short", "len", len(data))
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

	// Читаем client_repeat, если есть (после имени идёт 1 байт)
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

func (c *MeshCoreClient) parseDeviceInfo(data []byte) {
	if len(data) < 4 {
		c.logger.Warn("DeviceInfo too short", "len", len(data))
		return
	}
	c.logger.Debug("DeviceInfo received", "data", fmt.Sprintf("%x", data))
}

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
	// путь всегда 64 байта
	path := data[36:100]
	// Используем только pathLen байт (гарантированно не более 64)
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
	// last_modified (144:148) игнорируется

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
	// Минимальная длина: код + 6-байтный префикс + имя (хотя бы 1 байт) + нуль
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

func (c *MeshCoreClient) parseChannelInfo(data []byte) {
	if len(data) < 2 {
		return
	}
	index := int(data[1])
	// Имя может быть сокращённым, ищем нуль
	nameBytes := data[2:]
	nullIdx := bytes.IndexByte(nameBytes, 0)
	if nullIdx == -1 {
		nullIdx = len(nameBytes)
	}
	name := string(nameBytes[:nullIdx])
	// PSK находится после имени, 16 байт (если данные есть)
	var psk []byte
	if len(data) > 2+nullIdx+1 {
		pskStart := 2 + nullIdx + 1 // после нуля
		if pskStart+16 <= len(data) {
			psk = data[pskStart : pskStart+16]
		}
	}
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
	c.logger.Debug("Received parseOk OK")
	c.notifySent() // для канальных сообщений OK означает принятие команды
}

func (c *MeshCoreClient) parseLogRxData(data []byte) {
	c.logger.Debug("Log RX data received", "data", fmt.Sprintf("%x", data))
	// Можно игнорировать или использовать для отладки
}

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

func (c *MeshCoreClient) parseStats(data []byte) {
	if len(data) < 9 {
		c.logger.Warn("Stats too short", "len", len(data))
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

// updateMessagesWithFullKey заменяет префиксные ключи в сообщениях на полный ключ,
// когда становится известен полный публичный ключ контакта.
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

// SendMessageWithRetry отправляет сообщение с повторными попытками.
// waitForDelivery: true - ждать pushCodeSendConfirmed (доставка), false - ждать respCodeSent (отправка в эфир)
// maxRetries: максимальное число повторных попыток (0 - одна попытка без повторов)
// timeout: таймаут ожидания подтверждения после каждой отправки
func (c *MeshCoreClient) SendMessageWithRetry(contact *Contact, text string, waitForDelivery bool, maxRetries int, timeout time.Duration) error {
	defer time.Sleep(150 * time.Microsecond) // частая отправка приводит к пропаже сообщений

	c.logger.Debug("Sending private message with retry:", fmt.Sprintf("%x", contact.PublicKey), contact.Name, text, waitForDelivery)

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

// Для обратной совместимости оставляем старый SendMessage
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

	c.logger.Debug("Sending channel message with retry:", fmt.Sprintf("%v", channel.Index), channel.Name, text, waitForDelivery)

	if waitForDelivery {
		c.logger.Warn("Wait for delivery is unavailable for channels, will be override to False")
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
		if waitForDelivery {
			c.deliveredConfirmCh = confirmCh
		} else {
			c.sentConfirmCh = confirmCh
		}
		c.confirmMu.Unlock()

		// Ожидаем подтверждение или таймаут
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
	return c.sendGetStats(1) // statsTypeRadio
}

func (c *MeshCoreClient) RequestBattery() error {
	return c.sendGetBatteryAndStorage()
}

// SyncNextMessage запрашивает следующее сообщение из очереди устройства.
func (c *MeshCoreClient) SyncNextMessage() error {
	return c.sendSyncNextMessage()
}

// OnNoMoreMessages устанавливает коллбэк, вызываемый при получении respCodeNoMoreMessages.
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

// для чтения входящих сообщений
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

package meshcore_bt

import (
	"bytes"
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"meshbot/internal/historylog"
	"meshbot/internal/mqttbridge"

	"go.bug.st/serial"
	"tinygo.org/x/bluetooth"
)

// pendingRawPacket holds a raw RF packet from a 0x88 BLE frame while waiting
// to be correlated with the corresponding decoded 0x11 channel message frame.
// ErrDeviceRejected is returned by SendMessageWithRetry when the device
// explicitly rejects every send attempt (e.g. radio queue full, message too
// long). Callers should not retry with different confirmation settings because
// the failure is not about ACK delivery — the message was never queued.
var ErrDeviceRejected = fmt.Errorf("device rejected send")

type pendingRawPacket struct {
	rawData   []byte
	snr       float64
	rssi      float64
	arrivedAt time.Time
}

// pendingRawTTL is how long a raw packet waits for a matching decoded frame
// before being published as-is (without decoded channel fields).
const pendingRawTTL = 2 * time.Second

// ============================================================================
// Основной клиент
// ============================================================================

type MeshCoreClient struct {
	adapter   *bluetooth.Adapter
	device    *bluetooth.Device
	rxChar    *bluetooth.DeviceCharacteristic
	txChar    *bluetooth.DeviceCharacteristic
	connected bool
	address   bluetooth.Address
	mu        sync.RWMutex

	// Состояние
	selfInfo          *SelfInfo
	contacts          map[string]*Contact // ключ – hex(pubKey)
	syncLastmod       uint32              // max lastmod seen across all parsed contact frames
	pendingNewContact *Contact            // contact from last pushCodeNewAdvert, may not be stored on device yet
	channels          map[int]*Channel
	messages    map[string][]*Message // ключ – hex(senderKey)
	channelMsgs map[int][]*ChannelMessage
	stats       *CompanionRadioStats // накопленная статистика (все типы)

	// MQTT bridge
	mqtt *mqttbridge.MQTTBridge

	// Contact lookup function for MQTT message sender names
	contactLookupFunc func(pubKey []byte) string

	// pendingRaw holds raw RF packets from 0x88 frames waiting to be correlated
	// with a decoded 0x11 channel message frame. Both frames arrive sequentially
	// on the same BLE notification stream, so no mutex is needed — only the BLE
	// goroutine reads and writes this slice.
	pendingRaw []pendingRawPacket

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
	onAdvert         func(*Contact)

	// serialPort is non-nil when connected via USB/serial instead of BLE.
	serialPort     serial.Port
	serialPortPath string
	serialBaud     int

	// writeMu serializes raw writes — held only for the duration of the write
	// call. Prevents concurrent writes which cause timeouts.
	writeMu sync.Mutex

	// sendMu serializes full send operations (write + confirmation wait).
	// Only one Send*WithRetry can be in flight at a time.
	// The frame handler must never lock sendMu (it delivers confirmations).
	sendMu sync.Mutex

	// для ожидания подтверждения
	sentConfirmCh      chan error // для respCodeSent / respCodeOk
	deliveredConfirmCh chan error // для pushCodeSendConfirmed
	confirmMu          sync.Mutex


	// канал входящих сообщений
	incomingCh chan interface{}
	wg         sync.WaitGroup

	// Логгер
	logger  *slog.Logger
	histLog *historylog.Logger
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

// SetHistoryLogger attaches a history logger that records every incoming and
// outgoing message to per-conversation files.
func (c *MeshCoreClient) SetHistoryLogger(h *historylog.Logger) {
	c.histLog = h
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
	c.address = address
	// Закрываем старый doneCh, если он ещё не закрыт (например, после разрыва)
	select {
	case <-c.doneCh:
		// уже закрыт
	default:
		close(c.doneCh)
	}
	c.doneCh = make(chan struct{})
	c.mu.Unlock()

	// Drain any stale notifications buffered from the previous connection.
drainLoop:
	for {
		select {
		case <-c.notifyCh:
		default:
			break drainLoop
		}
	}

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
	time.Sleep(200 * time.Millisecond)

	if err := c.sendSetDeviceTime(uint32(time.Now().Unix())); err != nil {
		c.logger.Warn("Failed to sync device time", "error", err)
	} else {
		c.logger.Info("Device time synced")
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
	// Close the transport before waiting so any blocked Read() in serialReadLoop
	// (or BLE notification handler) returns immediately and the goroutine can exit.
	var closeErr error
	if c.serialPort != nil {
		c.logger.Info("Closing serial port")
		closeErr = c.serialPort.Close()
	} else if c.device != nil {
		c.logger.Info("Disconnecting from device")
		closeErr = c.device.Disconnect()
	}
	c.wg.Wait()
	close(c.incomingCh)
	return closeErr
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
	c.OnPacketSent(buf.Bytes())
	return messageID, nil
}

// подтверждение отправки
func (c *MeshCoreClient) notifySent() {
	c.confirmMu.Lock()
	defer c.confirmMu.Unlock()
	if c.sentConfirmCh != nil {
		c.sentConfirmCh <- nil
		c.sentConfirmCh = nil
	}
}

// подтверждение доставки
func (c *MeshCoreClient) notifyDelivered() {
	c.confirmMu.Lock()
	defer c.confirmMu.Unlock()
	if c.deliveredConfirmCh != nil {
		c.deliveredConfirmCh <- nil
		c.deliveredConfirmCh = nil
	}
}

// notifyErr сигнализирует об ошибке всем ожидающим каналам подтверждения.
func (c *MeshCoreClient) notifyErr(err error) {
	c.confirmMu.Lock()
	defer c.confirmMu.Unlock()
	if c.sentConfirmCh != nil {
		c.sentConfirmCh <- err
		c.sentConfirmCh = nil
	}
	if c.deliveredConfirmCh != nil {
		c.deliveredConfirmCh <- err
		c.deliveredConfirmCh = nil
	}
}

func errCodeName(code byte) string {
	switch code {
	case errCodeUnsupportedCmd:
		return "unsupported_cmd"
	case errCodeNotFound:
		return "not_found"
	case errCodeTableFull:
		return "table_full"
	case errCodeBadState:
		return "bad_state"
	case errCodeFileIOError:
		return "file_io_error"
	case errCodeIllegalArg:
		return "illegal_arg"
	default:
		return "unknown"
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
	if err := c.write(buf.Bytes()); err != nil {
		return err
	}
	c.OnPacketSent(buf.Bytes())
	return nil
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

func (c *MeshCoreClient) sendGetContactByKey(pubKey []byte) error {
	if len(pubKey) < pubKeySize {
		return fmt.Errorf("get contact by key requires full %d-byte public key", pubKeySize)
	}
	buf := new(bytes.Buffer)
	buf.WriteByte(cmdGetContactByKey)
	buf.Write(pubKey[:pubKeySize])
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
	if !c.connected {
		c.mu.RUnlock()
		return fmt.Errorf("not connected")
	}
	isSerial := c.serialPort != nil
	c.mu.RUnlock()

	var err error
	if isSerial {
		err = c.serialWriteFramed(data)
	} else {
		c.mu.RLock()
		if c.rxChar == nil {
			c.mu.RUnlock()
			return fmt.Errorf("not connected")
		}
		c.writeMu.Lock()
		_, err = c.rxChar.Write(data)
		c.writeMu.Unlock()
		c.mu.RUnlock()
	}
	if err != nil {
		c.logger.Error("Write failed", "error", err)
		c.markDisconnected()
	}
	return err
}

// serialWriteFramed wraps data in the MeshCore serial frame format:
// '<' + len_LSB + len_MSB + payload
func (c *MeshCoreClient) serialWriteFramed(data []byte) error {
	frame := make([]byte, 3+len(data))
	frame[0] = '<'
	binary.LittleEndian.PutUint16(frame[1:3], uint16(len(data)))
	copy(frame[3:], data)
	c.writeMu.Lock()
	_, err := c.serialPort.Write(frame)
	c.writeMu.Unlock()
	return err
}

// serialReadLoop reads framed packets from the serial port and feeds them into
// notifyCh. Frame format: '>' + len_LSB + len_MSB + payload (device→host).
func (c *MeshCoreClient) serialReadLoop() {
	defer c.wg.Done()

	const (
		stateIdle       = 0
		stateGotMarker  = 1
		stateGotLen1    = 2
		stateCollecting = 3
	)

	state := stateIdle
	var lenBuf [2]byte
	var frameLen int
	var frame []byte
	oneByte := make([]byte, 1)

	for {
		select {
		case <-c.doneCh:
			return
		default:
		}

		n, err := c.serialPort.Read(oneByte)
		if err != nil {
			if err == io.EOF {
				continue
			}
			select {
			case <-c.doneCh:
				return
			default:
				c.logger.Error("Serial read error", "error", err)
				c.markDisconnected()
				return
			}
		}
		if n == 0 {
			continue
		}
		b := oneByte[0]

		switch state {
		case stateIdle:
			if b == '>' {
				state = stateGotMarker
			}
		case stateGotMarker:
			lenBuf[0] = b
			state = stateGotLen1
		case stateGotLen1:
			lenBuf[1] = b
			frameLen = int(binary.LittleEndian.Uint16(lenBuf[:]))
			if frameLen == 0 || frameLen > 512 {
				c.logger.Warn("Serial frame: invalid length, resetting", "len", frameLen)
				state = stateIdle
				continue
			}
			frame = make([]byte, 0, frameLen)
			state = stateCollecting
		case stateCollecting:
			frame = append(frame, b)
			if len(frame) >= frameLen {
				frameCopy := make([]byte, len(frame))
				copy(frameCopy, frame)
				select {
				case c.notifyCh <- frameCopy:
				default:
					c.logger.Warn("Serial notification channel full, dropping frame")
				}
				state = stateIdle
			}
		}
	}
}

// markDisconnected transitions the client to a disconnected state after a
// write or read failure. It is safe to call concurrently; subsequent calls are no-ops.
func (c *MeshCoreClient) markDisconnected() {
	c.mu.Lock()
	if !c.connected {
		c.mu.Unlock()
		return
	}
	c.connected = false
	select {
	case <-c.doneCh:
	default:
		close(c.doneCh)
	}
	isSerial := c.serialPort != nil
	c.mu.Unlock()
	c.wg.Wait() // wait for goroutines to exit
	if isSerial {
		_ = c.serialPort.Close()
		c.logger.Warn("Serial connection lost; reconnection required")
	} else if c.device != nil {
		_ = c.device.Disconnect()
		c.logger.Warn("BLE connection lost; reconnection required")
	}
}

// IsConnected reports whether the client currently has an active BLE connection.
func (c *MeshCoreClient) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// Reconnect re-establishes the connection (BLE or serial) to the previously used address/port.
func (c *MeshCoreClient) Reconnect() error {
	c.mu.RLock()
	isSerial := c.serialPort != nil || c.serialPortPath != ""
	portPath := c.serialPortPath
	baud := c.serialBaud
	addr := c.address
	c.mu.RUnlock()
	if isSerial {
		return c.ConnectSerial(portPath, baud)
	}
	return c.Connect(addr)
}

// ConnectSerial opens a serial port and initialises the MeshCore protocol over it.
// portPath is the OS path to the serial device (e.g. "/dev/ttyUSB0", "COM3").
// baud should be 115200 for standard MeshCore firmware.
func (c *MeshCoreClient) ConnectSerial(portPath string, baud int) error {
	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return nil
	}
	c.serialPortPath = portPath
	c.serialBaud = baud
	select {
	case <-c.doneCh:
	default:
		close(c.doneCh)
	}
	c.doneCh = make(chan struct{})
	c.mu.Unlock()

	// Drain stale notifications from any previous connection.
drainLoop:
	for {
		select {
		case <-c.notifyCh:
		default:
			break drainLoop
		}
	}

	mode := &serial.Mode{BaudRate: baud}
	port, err := serial.Open(portPath, mode)
	if err != nil {
		return fmt.Errorf("open serial port %s: %w", portPath, err)
	}

	c.mu.Lock()
	c.serialPort = port
	c.connected = true
	c.mu.Unlock()

	c.logger.Info("Serial port opened", "port", portPath, "baud", baud)

	// Start the serial read loop (feeds notifyCh like BLE notifications do).
	c.wg.Add(1)
	go c.serialReadLoop()

	// Start the frame handler goroutine (same as BLE path).
	c.wg.Add(1)
	go c.frameHandler()

	if err := c.sendAppStart(); err != nil {
		c.Disconnect()
		return err
	}
	time.Sleep(200 * time.Millisecond)

	if err := c.sendDeviceQuery(); err != nil {
		c.Disconnect()
		return err
	}
	time.Sleep(200 * time.Millisecond)

	if err := c.sendSetDeviceTime(uint32(time.Now().Unix())); err != nil {
		c.logger.Warn("Failed to sync device time", "error", err)
	} else {
		c.logger.Info("Device time synced")
	}
	return nil
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
	pathLen := int(data[10])
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
	var contactPath []byte
	c.mu.RLock()
	for _, contact := range c.contacts {
		if len(contact.PublicKey) >= 6 && bytes.Equal(contact.PublicKey[:6], prefix) {
			fullKey = contact.PublicKey
			senderName = contact.Name
			contactPath = contact.Path
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
		PathLength: pathLen,
		PathBytes:  contactPath,
		MessageID:  fmt.Sprintf("%x-%d", prefix, timestampRaw),
	}

	c.mu.Lock()
	key := pubKeyHex(fullKey)
	c.messages[key] = append(c.messages[key], msg)
	c.mu.Unlock()

	c.logger.Info("Received contact message V3", "sender", senderName, "key", key, "text", text)

	if c.histLog != nil {
		if err := c.histLog.LogDMIncoming(senderName, text, msg.Timestamp); err != nil {
			c.logger.Error("historylog: failed to log incoming DM", "error", err)
		}
	}

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
	pathLen := int(data[5])
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
		PathLength:   pathLen,
		MessageID:    fmt.Sprintf("%d-%d", channelIndex, timestampRaw),
	}

	c.mu.Lock()
	c.channelMsgs[channelIndex] = append(c.channelMsgs[channelIndex], msg)
	c.mu.Unlock()

	c.logger.Info("Channel message V3 received",
		"channel", channelIndex, "sender", fmt.Sprintf("%q", senderName), "text", fmt.Sprintf("%q", text))


	if c.onChannelMessage != nil {
		c.onChannelMessage(msg)
	}

	// Publish to MQTT packets topic with decoded fields.
	// Correlate with the most recent pending 0x88 raw frame (same radio packet).
	c.mu.RLock()
	ch := c.channels[channelIndex]
	mqttBridge := c.mqtt
	c.mu.RUnlock()

	channelName := fmt.Sprintf("ch%d", channelIndex)
	var channelKeyHex string
	if ch != nil && ch.Name != "" {
		channelName = ch.Name
	}
	if ch != nil && len(ch.PSK) > 0 {
		channelKeyHex = hex.EncodeToString(ch.PSK)
	}

	if c.histLog != nil {
		if err := c.histLog.LogChannelIncoming(channelName, senderName, text, msg.Timestamp); err != nil {
			c.logger.Error("historylog: failed to log incoming channel message", "error", err)
		}
	}

	snr := float64(int8(data[1])) / 4.0
	rssi := 0.0 // V3 frame does not carry RSSI; use pending raw value if found

	// Find the oldest pending raw packet (0x88 arrives before 0x11, so oldest first).
	var rawData []byte
	for i := 0; i < len(c.pendingRaw); i++ {
		p := c.pendingRaw[i]
		if time.Since(p.arrivedAt) <= pendingRawTTL {
			rawData = p.rawData
			rssi = p.rssi
			// Remove the matched entry so it is not published again.
			c.pendingRaw = append(c.pendingRaw[:i], c.pendingRaw[i+1:]...)
			break
		}
	}

	if mqttBridge != nil {
		mqttBridge.QueueChannelPacket(rawData, false, snr, rssi, channelIndex, channelName, senderName, text, channelKeyHex)
	}

	// Extract path bytes from raw RF packet so the bot can show repeater IDs.
	if pathBytes, hopCount := extractPathFromRaw(rawData); len(pathBytes) > 0 {
		msg.PathBytes = pathBytes
		msg.PathLength = hopCount
	}

	// отправляем в канал входящих сообщений
	select {
	case c.incomingCh <- msg:
	default:
		c.logger.Warn("Incoming message channel full, dropping message")
	}
}

// extractPathFromRaw parses the path bytes out of a raw RF packet.
//
// Wire format (from Packet.cpp writeTo()):
//
//	[header(1)] [transport_codes(4) — only for TRANSPORT routes] [path_len(1)] [path...] [payload...]
//
// path_len byte encoding (from Packet.h):
//
//	hash_count = path_len & 0x3F   (bits 0–5, number of hops)
//	hash_size  = (path_len >> 6) + 1  (bits 6–7 → 1, 2, or 3 bytes per hop)
//
// Returns the raw path bytes and the hop count. Returns nil, 0 when data is
// too short or contains no hops.
func extractPathFromRaw(data []byte) (pathBytes []byte, hopCount int) {
	if len(data) < 2 {
		return nil, 0
	}
	routeType := data[0] & 0x03
	hasTransportCodes := routeType == 0x00 || routeType == 0x03
	headerLen := 2
	if hasTransportCodes {
		headerLen = 6
	}
	if len(data) < headerLen {
		return nil, 0
	}
	pathLenByte := data[headerLen-1]
	hashCount := int(pathLenByte & 0x3F)
	hashSize := int((pathLenByte>>6)&0x03) + 1
	if hashCount == 0 {
		return nil, 0
	}
	end := headerLen + hashCount*hashSize
	if end > len(data) {
		return nil, 0
	}
	return data[headerLen:end], hashCount
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
	pathLen := int(data[7])
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
	var contactPath []byte
	c.mu.RLock()
	for _, contact := range c.contacts {
		if len(contact.PublicKey) >= 6 && bytes.Equal(contact.PublicKey[:6], prefix) {
			fullKey = contact.PublicKey
			contactPath = contact.Path
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
		PathBytes:  contactPath,
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
	pathLen := int(data[2])
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
		PathLength:   pathLen,
		MessageID:    fmt.Sprintf("%d-%d", channelIndex, timestampRaw),
	}

	c.mu.Lock()
	c.channelMsgs[channelIndex] = append(c.channelMsgs[channelIndex], msg)
	c.mu.Unlock()

	c.logger.Info("Channel message received (V1)",
		"channel", channelIndex, "sender", fmt.Sprintf("%q", senderName), "text", fmt.Sprintf("%q", text))


	if c.onChannelMessage != nil {
		c.onChannelMessage(msg)
	}

	// Publish to MQTT packets topic with decoded fields.
	c.mu.RLock()
	ch := c.channels[channelIndex]
	mqttBridge := c.mqtt
	c.mu.RUnlock()

	channelName := fmt.Sprintf("ch%d", channelIndex)
	var channelKeyHex string
	if ch != nil && ch.Name != "" {
		channelName = ch.Name
	}
	if ch != nil && len(ch.PSK) > 0 {
		channelKeyHex = hex.EncodeToString(ch.PSK)
	}

	// Correlate with the oldest pending 0x88 raw frame (arrives before decoded frame).
	var rawData []byte
	var snr, rssi float64
	for i := 0; i < len(c.pendingRaw); i++ {
		p := c.pendingRaw[i]
		if time.Since(p.arrivedAt) <= pendingRawTTL {
			rawData = p.rawData
			snr = p.snr
			rssi = p.rssi
			c.pendingRaw = append(c.pendingRaw[:i], c.pendingRaw[i+1:]...)
			break
		}
	}

	if mqttBridge != nil {
		mqttBridge.QueueChannelPacket(rawData, false, snr, rssi, channelIndex, channelName, senderName, text, channelKeyHex)
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
		if len(data) >= 5 {
			total := binary.LittleEndian.Uint32(data[1:5])
			c.logger.Info("Contact sync started", "device_total", total)
		}
	case respCodeContact:
		if len(data) < contactFrameSize {
			c.parseContactV3(data)
		} else {
			c.parseContact(data)
		}
	case respCodeEndOfContacts:
		c.mu.RLock()
		cached := len(c.contacts)
		c.mu.RUnlock()
		c.logger.Info("Contact sync complete", "contacts", cached)
	case respCodeChannelInfo:
		if len(data) < 2 {
			c.logger.Warn("ChannelInfo too short", "len", len(data))
			return
		}
		c.parseChannelInfo(data)
	case respCodeContactMsgRecv:
		c.parseContactMessage(data)
		_ = c.sendSyncNextMessage()
	case respCodeContactMsgRecvV3:
		c.parseContactMessageV3(data)
		_ = c.sendSyncNextMessage()
	case respCodeChannelMsgRecv:
		c.parseChannelMessage(data)
		_ = c.sendSyncNextMessage()
	case respCodeChannelMsgRecvV3:
		c.parseChannelMessageV3(data)
		_ = c.sendSyncNextMessage()
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
		var errCode byte
		var errName string
		if len(data) >= 2 {
			errCode = data[1]
			errName = errCodeName(errCode)
		}
		c.logger.Error("Received error response", "err_code", errCode, "err_name", errName)
		c.notifyErr(fmt.Errorf("device error %d (%s)", errCode, errName))
	case pushCodeAdvert:
		c.parseAdvert(data)
	case pushCodeNewAdvert:
		c.parseNewAdvert(data)
	case pushCodeContactDeleted:
		c.parseContactDeleted(data)
	case pushCodeContactsFull:
		c.handleContactsFull()
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
// Формат (из MyMesh.cpp, CMD_DEVICE_QUERY):
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
	lastMod := binary.LittleEndian.Uint32(data[144:148])

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
		LastMod:    lastMod,
	}

	c.updateMessagesWithFullKey(contact.PublicKey)

	c.mu.Lock()
	c.contacts[pubKeyHex(pubKey)] = contact
	if lastMod > c.syncLastmod {
		c.syncLastmod = lastMod
	}
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
	c.logger.Info("Advert received", "data", fmt.Sprintf("%x", data))

	// Frame format: [code(1)][pub_key(32)]  — total 33 bytes.
	// The firmware pushes PUSH_CODE_ADVERT (0x80) with the full 32-byte
	// public key of the node whose advert was just received over-the-air.
	// SNR/RSSI are not included in this push frame.
	if len(data) < 1+pubKeySize {
		c.logger.Warn("parseAdvert: frame too short", "len", len(data), "need", 1+pubKeySize)
		return
	}

	pubKey := make([]byte, pubKeySize)
	copy(pubKey, data[1:1+pubKeySize])

	// Look up the full contact from the contacts cache so that we can
	// include name, type, GPS and path-length in the MQTT advert message.
	c.mu.RLock()
	contact := c.contacts[pubKeyHex(pubKey)]
	c.mu.RUnlock()

	if contact == nil {
		// Contact not yet in cache (advert arrived before CMD_GET_CONTACTS
		// response or auto-add is disabled).  Build a minimal stub so MQTT
		// at least gets the public key.
		c.logger.Warn("parseAdvert: contact not in cache, using stub",
			"pub_key", fmt.Sprintf("%x", pubKey))
		contact = &Contact{
			PublicKey: pubKey,
			LastSeen:  time.Now(),
		}
	} else {
		// The device has updated this contact's data (GPS, name, path) on its
		// side. Fetch just this one contact by key — avoids triggering the
		// full GET_CONTACTS iteration protocol on every advert.
		if err := c.sendGetContactByKey(pubKey); err != nil {
			c.logger.Warn("parseAdvert: failed to refresh contact", "error", err)
		}
	}

	c.mu.RLock()
	mqttBridge := c.mqtt
	c.mu.RUnlock()

	if mqttBridge != nil {
		// Correlate with the oldest pending 0x88 raw frame (arrives before 0x80).
		var rawData []byte
		var snr, rssi float64
		for i := 0; i < len(c.pendingRaw); i++ {
			p := c.pendingRaw[i]
			if time.Since(p.arrivedAt) <= pendingRawTTL {
				rawData = p.rawData
				snr = p.snr
				rssi = p.rssi
				c.pendingRaw = append(c.pendingRaw[:i], c.pendingRaw[i+1:]...)
				break
			}
		}
		mqttBridge.QueuePacket(rawData, false, snr, rssi)
	}

	if c.onAdvert != nil {
		c.onAdvert(contact)
	}
}

// parseNewAdvert handles PUSH_CODE_NEW_ADVERT (0x8A). The frame format is
// identical to RESP_CODE_CONTACT, so we reuse parseContact to update the
// cache. We also remember the contact in pendingNewContact: if the device
// follows up with PUSH_CODE_CONTACTS_FULL (0x90), the contact was not stored
// on the device and we need to add it manually after evicting the oldest.
func (c *MeshCoreClient) parseNewAdvert(data []byte) {
	c.parseContact(data)

	// Retrieve the contact that parseContact just stored.
	if len(data) < 33 {
		return
	}
	pubKey := data[1:33]
	c.mu.Lock()
	c.pendingNewContact = c.contacts[pubKeyHex(pubKey)]
	c.mu.Unlock()
	c.logger.Info("New contact discovered", "pub_key", fmt.Sprintf("%x", pubKey))
}

// parseContactDeleted handles PUSH_CODE_CONTACT_DELETED (0x8F).
// Frame: [code(1)][pub_key(32)]. Removes the contact from the local cache.
func (c *MeshCoreClient) parseContactDeleted(data []byte) {
	if len(data) < 1+pubKeySize {
		c.logger.Warn("parseContactDeleted: frame too short", "len", len(data))
		return
	}
	pubKey := data[1 : 1+pubKeySize]
	keyHex := pubKeyHex(pubKey)
	c.mu.Lock()
	delete(c.contacts, keyHex)
	c.mu.Unlock()
	c.logger.Info("Contact deleted by device", "pub_key", fmt.Sprintf("%x", pubKey))
}

// handleContactsFull handles PUSH_CODE_CONTACTS_FULL (0x90). The device has
// hit its 350-contact limit and could not store the most recent new contact.
// We evict the oldest contact (by last activity) and then write the pending
// new contact to the device so it gets a proper slot.
func (c *MeshCoreClient) handleContactsFull() {
	c.mu.Lock()
	pending := c.pendingNewContact
	c.pendingNewContact = nil

	// Find the oldest contact by activity, skipping the pending new one.
	var oldest *Contact
	for _, ct := range c.contacts {
		if pending != nil && pubKeyHex(ct.PublicKey) == pubKeyHex(pending.PublicKey) {
			continue
		}
		if oldest == nil {
			oldest = ct
			continue
		}
		oldestActivity := oldest.LastSeen
		if !oldest.LastMessageAt.IsZero() && oldest.LastMessageAt.After(oldestActivity) {
			oldestActivity = oldest.LastMessageAt
		}
		ctActivity := ct.LastSeen
		if !ct.LastMessageAt.IsZero() && ct.LastMessageAt.After(ctActivity) {
			ctActivity = ct.LastMessageAt
		}
		if ctActivity.Before(oldestActivity) {
			oldest = ct
		}
	}
	if oldest != nil {
		delete(c.contacts, pubKeyHex(oldest.PublicKey))
	}
	c.mu.Unlock()

	if oldest == nil {
		c.logger.Warn("handleContactsFull: no contact to evict")
		return
	}

	c.logger.Info("Contacts full: evicting oldest contact",
		"name", oldest.Name, "pub_key", fmt.Sprintf("%x", oldest.PublicKey),
		"last_seen", oldest.LastSeen)

	if err := c.sendRemoveContact(oldest.PublicKey); err != nil {
		c.logger.Error("handleContactsFull: failed to remove contact", "error", err)
		return
	}

	if pending == nil {
		return
	}
	if err := c.sendUpdateContact(pending); err != nil {
		c.logger.Error("handleContactsFull: failed to add new contact", "error", err)
	}
}

// buildGRPTXTPacket constructs a valid MeshCore GRP_TXT RF packet from plaintext
// fields. This lets us publish outgoing channel messages to MQTT as TX packets so
// the analyzer server can decode and display them like any other received packet.
//
// Encryption: AES-128-ECB with the 16-byte channel PSK.
// MAC:        HMAC-SHA256(psk‖zeros16, ciphertext)[:2].
// Wire:       [header=0x15][path_len=0x00][channel_hash(1)][mac(2)][ciphertext…]
func buildGRPTXTPacket(psk []byte, senderName, text string) []byte {
	if len(psk) < 16 {
		return nil
	}
	key := psk[:16]

	// Plaintext: timestamp(4 LE) + flags(1) + "sender: text"
	ts := uint32(time.Now().Unix())
	msg := senderName + ": " + text
	plain := make([]byte, 5+len(msg))
	binary.LittleEndian.PutUint32(plain[0:4], ts)
	plain[4] = 0x00
	copy(plain[5:], msg)

	// Pad to AES block boundary
	if pad := aes.BlockSize - (len(plain) % aes.BlockSize); pad != aes.BlockSize {
		plain = append(plain, make([]byte, pad)...)
	}

	// AES-128-ECB encrypt (block by block, no IV)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	ct := make([]byte, len(plain))
	for i := 0; i < len(plain); i += aes.BlockSize {
		block.Encrypt(ct[i:i+aes.BlockSize], plain[i:i+aes.BlockSize])
	}

	// HMAC-SHA256 MAC: secret = key(16) + zeros(16)
	secret := make([]byte, 32)
	copy(secret, key)
	h := hmac.New(sha256.New, secret)
	h.Write(ct)
	mac := h.Sum(nil)[:2]

	// Channel hash = SHA256(PSK)[0]
	hashSum := sha256.Sum256(key)

	// Wire: [0x15 header][0x00 path_len][channel_hash][mac(2)][ciphertext…]
	// header = ROUTE_TYPE_FLOOD(0x01) | PAYLOAD_TYPE_GRP_TXT(0x05)<<2 = 0x15
	pkt := make([]byte, 0, 3+len(mac)+len(ct))
	pkt = append(pkt, 0x15, 0x00, hashSum[0])
	pkt = append(pkt, mac...)
	pkt = append(pkt, ct...)
	return pkt
}

// publishTXChannelPacket builds a synthetic outgoing GRP_TXT RF packet and
// queues it for MQTT publishing as a TX packet. This makes our outgoing channel
// messages visible on analyzer servers exactly like packets from any other device.
func (c *MeshCoreClient) publishTXChannelPacket(channel *Channel, text string) {
	c.mu.RLock()
	mqttBridge := c.mqtt
	selfInfo := c.selfInfo
	c.mu.RUnlock()

	if mqttBridge == nil || selfInfo == nil {
		return
	}
	if len(channel.PSK) == 0 {
		return
	}

	senderName := selfInfo.Name
	rawPkt := buildGRPTXTPacket(channel.PSK, senderName, text)
	if rawPkt == nil {
		c.logger.Warn("publishTXChannelPacket: failed to build packet")
		return
	}

	channelName := fmt.Sprintf("ch%d", channel.Index)
	if channel.Name != "" {
		channelName = channel.Name
	}
	channelKeyHex := hex.EncodeToString(channel.PSK[:16])

	mqttBridge.QueueChannelPacket(rawPkt, true, 0, 0, channel.Index, channelName, senderName, text, channelKeyHex)
}

func (c *MeshCoreClient) parseOk(data []byte) {
	c.logger.Debug("Received OK")
	c.notifySent() // для канальных сообщений OK означает принятие команды
}

func (c *MeshCoreClient) parseLogRxData(data []byte) {
	// Frame: [0x88][SNR*4 as int8][RSSI as int8][raw RF bytes...]
	if len(data) < 4 {
		c.logger.Debug("Log RX data too short", "len", len(data))
		return
	}
	snr := float64(int8(data[1])) / 4.0
	rssi := float64(int8(data[2]))
	rawData := make([]byte, len(data)-3)
	copy(rawData, data[3:])

	c.logger.Debug("Log RX data", "snr", snr, "rssi", rssi, "raw_len", len(rawData))

	// Flush any stale pending raw packets that never got a matching 0x11 frame.
	c.flushStalePendingRaw()

	// Store in pending; parseChannelMessageV3 will correlate and publish.
	c.pendingRaw = append(c.pendingRaw, pendingRawPacket{
		rawData:   rawData,
		snr:       snr,
		rssi:      rssi,
		arrivedAt: time.Now(),
	})
	if len(c.pendingRaw) > 8 {
		c.pendingRaw = c.pendingRaw[len(c.pendingRaw)-8:]
	}
}

// flushStalePendingRaw publishes any raw packets that have waited longer than
// pendingRawTTL without a matching decoded channel message frame.
func (c *MeshCoreClient) flushStalePendingRaw() {
	if len(c.pendingRaw) == 0 {
		return
	}
	c.mu.RLock()
	mqttBridge := c.mqtt
	c.mu.RUnlock()

	now := time.Now()
	remaining := c.pendingRaw[:0]
	for _, p := range c.pendingRaw {
		if now.Sub(p.arrivedAt) > pendingRawTTL {
			if mqttBridge != nil {
				mqttBridge.QueuePacket(p.rawData, false, p.snr, p.rssi)
			}
		} else {
			remaining = append(remaining, p)
		}
	}
	c.pendingRaw = remaining
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

// parseStats разбирает RESP_CODE_STATS (0x18) для всех типов статистики.
//
// Форматы (из MeshCore/docs/stats_binary_frames.md):
//
//	STATS_TYPE_CORE (0):    [code(1)][type(1)][battery_mv(2)][uptime_secs(4)][errors(2)][queue_len(1)] = 11 bytes
//	STATS_TYPE_RADIO (1):   [code(1)][type(1)][noise_floor(2)][last_rssi(1)][last_snr(1)][tx_air_secs(4)][rx_air_secs(4)] = 14 bytes
//	STATS_TYPE_PACKETS (2): [code(1)][type(1)][recv(4)][sent(4)][flood_tx(4)][direct_tx(4)][flood_rx(4)][direct_rx(4)] = 26-30 bytes
func (c *MeshCoreClient) parseStats(data []byte) {
	if len(data) < 2 {
		c.logger.Warn("Stats too short", "len", len(data))
		return
	}

	// Get or create stats object (accumulate all stats types)
	c.mu.Lock()
	if c.stats == nil {
		c.stats = &CompanionRadioStats{}
	}
	stats := c.stats
	stats.LastUpdate = time.Now()
	c.mu.Unlock()

	statsType := data[1]
	switch statsType {
	case STATS_TYPE_CORE: // 0 - Core stats
		// [battery_mv(2)][uptime_secs(4)][errors(2)][queue_len(1)] = 9 bytes after header
		if len(data) < 11 {
			c.logger.Warn("Stats CORE too short", "len", len(data))
			return
		}
		stats.BatteryMv = binary.LittleEndian.Uint16(data[2:4])
		stats.UptimeSecs = binary.LittleEndian.Uint32(data[4:8])
		stats.Errors = binary.LittleEndian.Uint16(data[8:10])
		stats.QueueLen = data[10]
		c.logger.Debug("Stats CORE parsed",
			"battery_mv", stats.BatteryMv,
			"uptime_secs", stats.UptimeSecs,
			"errors", stats.Errors,
			"queue_len", stats.QueueLen)

	case STATS_TYPE_RADIO: // 1 - Radio stats
		// [noise_floor(2)][last_rssi(1)][last_snr(1)][tx_air_secs(4)][rx_air_secs(4)] = 12 bytes after header
		if len(data) < 14 {
			c.logger.Warn("Stats RADIO too short", "len", len(data))
			return
		}
		noiseFloor := int16(binary.LittleEndian.Uint16(data[2:4]))
		stats.NoiseFloorDbm = int8(noiseFloor)
		stats.LastRssi = int8(data[4])
		stats.LastSnrDb = float32(int8(data[5])) / 4.0
		stats.TxAirSecs = binary.LittleEndian.Uint32(data[6:10])
		stats.RxAirSecs = binary.LittleEndian.Uint32(data[10:14])
		c.logger.Debug("Stats RADIO parsed",
			"noise_floor", stats.NoiseFloorDbm,
			"last_rssi", stats.LastRssi,
			"last_snr", stats.LastSnrDb,
			"tx_air_secs", stats.TxAirSecs,
			"rx_air_secs", stats.RxAirSecs)

	case STATS_TYPE_PACKETS: // 2 - Packet stats
		// [recv(4)][sent(4)][flood_tx(4)][direct_tx(4)][flood_rx(4)][direct_rx(4)][recv_errors(4)] = 26-30 bytes after header
		if len(data) < 28 {
			c.logger.Warn("Stats PACKETS too short", "len", len(data))
			return
		}
		stats.PacketsRecv = binary.LittleEndian.Uint32(data[2:6])
		stats.PacketsSent = binary.LittleEndian.Uint32(data[6:10])
		stats.PacketsFloodTx = binary.LittleEndian.Uint32(data[10:14])
		stats.PacketsDirectTx = binary.LittleEndian.Uint32(data[14:18])
		stats.PacketsFloodRx = binary.LittleEndian.Uint32(data[18:22])
		stats.PacketsDirectRx = binary.LittleEndian.Uint32(data[22:26])
		// recv_errors present only in 30-byte frame
		if len(data) >= 30 {
			stats.PacketsRecvErrors = binary.LittleEndian.Uint32(data[26:30])
		}
		c.logger.Debug("Stats PACKETS parsed",
			"recv", stats.PacketsRecv,
			"sent", stats.PacketsSent,
			"flood_tx", stats.PacketsFloodTx,
			"direct_tx", stats.PacketsDirectTx,
			"flood_rx", stats.PacketsFloodRx,
			"direct_rx", stats.PacketsDirectRx,
			"recv_errors", stats.PacketsRecvErrors)

	default:
		c.logger.Warn("Stats: unknown stats_type", "type", statsType)
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
//
// waitForDelivery: ждать pushCodeSendConfirmed (true) или respCodeSent (false).
// savedPathTries:  сколько попыток сделать с сохранённым путём (минимум 1).
// resetPathTries:  сколько дополнительных попыток сделать со сбросом пути.
// timeout:         таймаут ожидания подтверждения для каждой попытки.
//
// Пример: savedPathTries=3, resetPathTries=3 — сначала 3 попытки по сохранённому
// пути, затем ещё 3 с принудительным сбросом пути перед каждой попыткой.
func (c *MeshCoreClient) SendMessageWithRetry(contact *Contact, text string, waitForDelivery bool, savedPathTries, resetPathTries int, timeout time.Duration) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	defer time.Sleep(2 * time.Second) // частая отправка приводит к пропаже сообщений

	if savedPathTries < 1 {
		savedPathTries = 1
	}
	totalAttempts := savedPathTries + resetPathTries

	c.logger.Info("Sending private message with retry",
		"key", fmt.Sprintf("%x", contact.PublicKey), "name", contact.Name,
		"text", text, "waitForDelivery", waitForDelivery,
		"savedPathTries", savedPathTries, "resetPathTries", resetPathTries)

	for attempt := 0; attempt < totalAttempts; attempt++ {
		if attempt >= savedPathTries {
			if contact.PathLength != pathLengthFlood {
				c.logger.Info("Resetting contact path before retry",
					"attempt", attempt, "name", contact.Name, "key", fmt.Sprintf("%x", contact.PublicKey[:6]))
				if err := c.sendResetPath(contact.PublicKey); err != nil {
					c.logger.Warn("Path reset failed", "error", err)
				}
				time.Sleep(1 * time.Second)
			} else {
				c.logger.Debug("Skipping path reset, contact is flood-routed",
					"attempt", attempt, "name", contact.Name)
			}
		}

		timestamp := uint32(time.Now().Unix())
		_, err := c.sendTextMessage(contact.PublicKey, text, byte(attempt), timestamp)
		if err != nil {
			if attempt == totalAttempts-1 {
				return fmt.Errorf("send failed after %d attempts: %w", totalAttempts, err)
			}
			time.Sleep(timeout)
			continue
		}

		// Создаём канал для ожидания
		confirmCh := make(chan error, 1)
		c.confirmMu.Lock()
		if waitForDelivery {
			c.deliveredConfirmCh = confirmCh
		} else {
			c.sentConfirmCh = confirmCh
		}
		c.confirmMu.Unlock()

		select {
		case sendErr := <-confirmCh:
			if sendErr != nil {
				c.logger.Warn("Device rejected message, retrying",
					"error", sendErr, "attempt", attempt, "name", contact.Name)
				if attempt == totalAttempts-1 {
					return fmt.Errorf("%w after %d attempts: %v", ErrDeviceRejected, totalAttempts, sendErr)
				}
				time.Sleep(2 * time.Second)
				continue
			}
			if c.histLog != nil {
				if err := c.histLog.LogDMOutgoing(contact.Name, text, time.Now()); err != nil {
					c.logger.Error("historylog: failed to log outgoing DM", "error", err)
				}
			}
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
			if attempt == totalAttempts-1 {
				return fmt.Errorf("no %s confirmation after %d attempts (%d saved-path, %d reset-path)",
					map[bool]string{true: "delivery", false: "sent"}[waitForDelivery],
					totalAttempts, savedPathTries, resetPathTries)
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

// SendChannelMessageWithRetry sends a message to a channel with retries.
//
// Confirmation strategy: channel messages have no delivery ACKs and the
// firmware's deduplication prevents the device from notifying the companion
// app about RF repeats of its own messages. The best available confirmation
// is respCodeOk — the device accepted the command and queued the packet for
// RF transmission.
//
// waitForDelivery is accepted for API compatibility but ignored.
// maxRetries: maximum number of additional attempts (0 = single send).
// timeout: how long to wait for the device OK per attempt.
func (c *MeshCoreClient) SendChannelMessageWithRetry(channel *Channel, text string, waitForDelivery bool, maxRetries int, timeout time.Duration) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	defer time.Sleep(150 * time.Millisecond) // avoid flooding the device

	c.logger.Info("Sending channel message with retry",
		"channel", channel.Index, "name", channel.Name, "text", text)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
		}

		// Register BEFORE the BLE write so we cannot miss a fast response.
		confirmCh := make(chan error, 1)
		c.confirmMu.Lock()
		c.sentConfirmCh = confirmCh
		c.confirmMu.Unlock()

		if err := c.sendChannelTextMessage(byte(channel.Index), text); err != nil {
			c.confirmMu.Lock()
			if c.sentConfirmCh == confirmCh {
				c.sentConfirmCh = nil
			}
			c.confirmMu.Unlock()
			if attempt == maxRetries {
				return fmt.Errorf("send failed after %d attempts: %w", maxRetries+1, err)
			}
			continue
		}

		select {
		case sendErr := <-confirmCh:
			if sendErr != nil {
				c.logger.Warn("Device rejected channel message, retrying",
					"error", sendErr, "channel", channel.Name, "attempt", attempt)
				if attempt == maxRetries {
					return fmt.Errorf("channel message rejected after %d attempts: %w", maxRetries+1, sendErr)
				}
				continue
			}
			c.logger.Info("Channel message queued by device",
				"channel", channel.Name, "attempt", attempt)
			c.publishTXChannelPacket(channel, text)
			if c.histLog != nil {
				if err := c.histLog.LogChannelOutgoing(channel.Name, text, time.Now()); err != nil {
					c.logger.Error("historylog: failed to log outgoing channel message", "error", err)
				}
			}
			return nil
		case <-time.After(timeout):
			c.confirmMu.Lock()
			if c.sentConfirmCh == confirmCh {
				c.sentConfirmCh = nil
			}
			c.confirmMu.Unlock()
			c.logger.Warn("Channel message: no device OK, retrying",
				"channel", channel.Name, "attempt", attempt)
		}
	}
	return fmt.Errorf("channel message not accepted by device after %d attempts", maxRetries+1)
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

func (c *MeshCoreClient) SendSelfAdvert(flood bool) error {
	floodByte := byte(0)
	if flood {
		floodByte = 1
	}
	return c.write([]byte{cmdSendSelfAdvert, floodByte})
}

func (c *MeshCoreClient) Reboot() error {
	return c.sendReboot()
}

// RequestStats requests all stats types from the device (Core, Radio, Packets).
// Stats are accumulated and returned via the OnStats callback.
func (c *MeshCoreClient) RequestAllStats() error {
	if err := c.sendGetStats(STATS_TYPE_CORE); err != nil {
		return err
	}
	time.Sleep(100 * time.Millisecond)
	if err := c.sendGetStats(STATS_TYPE_RADIO); err != nil {
		return err
	}
	time.Sleep(100 * time.Millisecond)
	return c.sendGetStats(STATS_TYPE_PACKETS)
}

// RequestStats requests radio stats (legacy method).
func (c *MeshCoreClient) RequestStats() error {
	return c.sendGetStats(STATS_TYPE_RADIO)
}

// GetStats returns the current accumulated stats.
// Returns nil if no stats have been received yet.
func (c *MeshCoreClient) GetStats() *CompanionRadioStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stats
}

// FormatStats returns a human-readable string of all stats.
func (c *MeshCoreClient) FormatStats() string {
	stats := c.GetStats()
	if stats == nil {
		return "Stats: no data"
	}

	var b strings.Builder

	// Header
	b.WriteString("┌─ Radio Stats ─────────────────────────────\n")

	// Core stats
	b.WriteString(fmt.Sprintf("│ Battery:    %d mV (%.2f V)\n", stats.BatteryMv, float64(stats.BatteryMv)/1000.0))
	b.WriteString(fmt.Sprintf("│ Uptime:     %s\n", formatDuration(stats.UptimeSecs)))
	b.WriteString(fmt.Sprintf("│ Queue:      %d packets\n", stats.QueueLen))
	if stats.Errors > 0 {
		b.WriteString(fmt.Sprintf("│ Errors:     0x%04X\n", stats.Errors))
	}

	// Radio stats
	b.WriteString(fmt.Sprintf("│ Noise:      %d dBm\n", stats.NoiseFloorDbm))
	b.WriteString(fmt.Sprintf("│ RSSI:       %d dBm\n", stats.LastRssi))
	b.WriteString(fmt.Sprintf("│ SNR:        %.1f dB\n", stats.LastSnrDb))
	b.WriteString(fmt.Sprintf("│ TX Airtime: %s\n", formatDuration(stats.TxAirSecs)))
	b.WriteString(fmt.Sprintf("│ RX Airtime: %s\n", formatDuration(stats.RxAirSecs)))

	// Packet stats
	b.WriteString("├─ Packets ──────────────────────────────────\n")
	b.WriteString(fmt.Sprintf("│ Recv:       %d (flood: %d, direct: %d)\n", stats.PacketsRecv, stats.PacketsFloodRx, stats.PacketsDirectRx))
	b.WriteString(fmt.Sprintf("│ Sent:       %d (flood: %d, direct: %d)\n", stats.PacketsSent, stats.PacketsFloodTx, stats.PacketsDirectTx))
	if stats.PacketsRecvErrors > 0 {
		b.WriteString(fmt.Sprintf("│ RX Errors:  %d\n", stats.PacketsRecvErrors))
	}

	// Footer
	if !stats.LastUpdate.IsZero() {
		b.WriteString(fmt.Sprintf("└─ Updated: %s", stats.LastUpdate.Format("15:04:05")))
	} else {
		b.WriteString("└─")
	}

	return b.String()
}

// formatDuration converts seconds to human-readable duration string.
func formatDuration(secs uint32) string {
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	if secs < 3600 {
		return fmt.Sprintf("%dm %ds", secs/60, secs%60)
	}
	if secs < 86400 {
		return fmt.Sprintf("%dh %dm %ds", secs/3600, (secs%3600)/60, secs%60)
	}
	return fmt.Sprintf("%dd %dh %dm", secs/86400, (secs%86400)/3600, (secs%3600)/60)
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
func (c *MeshCoreClient) OnAdvert(f func(*Contact)) {
	c.onAdvert = f
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

// ============================================================================
// MQTT Integration
// ============================================================================

// MQTTBridge returns the MQTT bridge instance (nil if not configured).
func (c *MeshCoreClient) MQTTBridge() *mqttbridge.MQTTBridge {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mqtt
}

// SetContactLookupFunc sets a function to lookup contact names by public key.
// This is used by the MQTT bridge to provide meaningful sender names in messages.
func (c *MeshCoreClient) SetContactLookupFunc(f func(pubKey []byte) string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.contactLookupFunc = f
}

// toDeviceInfo converts SelfInfo to mqttbridge.DeviceInfo.
func toDeviceInfo(info *SelfInfo) *mqttbridge.DeviceInfo {
	if info == nil {
		return nil
	}
	return &mqttbridge.DeviceInfo{
		PublicKey: info.PublicKey,
		Name:      info.Name,
		TxPower:   info.TxPower,
		Latitude:  info.Latitude,
		Longitude: info.Longitude,
		FreqHz:    info.FreqHz,
		BwHz:      info.BwHz,
		Sf:        info.Sf,
		Cr:        info.Cr,
	}
}

// toDeviceStats converts CompanionRadioStats to mqttbridge.DeviceStats.
func toDeviceStats(stats *CompanionRadioStats) *mqttbridge.DeviceStats {
	if stats == nil {
		return nil
	}
	return &mqttbridge.DeviceStats{
		BatteryMv:         stats.BatteryMv,
		UptimeSecs:        stats.UptimeSecs,
		Errors:            stats.Errors,
		QueueLen:          stats.QueueLen,
		TxAirSecs:         stats.TxAirSecs,
		RxAirSecs:         stats.RxAirSecs,
		NoiseFloorDbm:     stats.NoiseFloorDbm,
		LastRssi:          stats.LastRssi,
		LastSnrDb:         stats.LastSnrDb,
		PacketsRecv:       stats.PacketsRecv,
		PacketsSent:       stats.PacketsSent,
		PacketsFloodTx:    stats.PacketsFloodTx,
		PacketsDirectTx:   stats.PacketsDirectTx,
		PacketsFloodRx:    stats.PacketsFloodRx,
		PacketsDirectRx:   stats.PacketsDirectRx,
		PacketsRecvErrors: stats.PacketsRecvErrors,
	}
}

// SetMQTTConfig configures and starts the MQTT bridge.
// Returns the MQTTBridge instance or nil if configuration is invalid.
func (c *MeshCoreClient) SetMQTTConfig(config *mqttbridge.MQTTConfig) (*mqttbridge.MQTTBridge, error) {
	if config == nil {
		return nil, fmt.Errorf("mqtt config is required")
	}

	// CRITICAL: Stop existing MQTT bridge before creating new one
	// This prevents connection conflicts and stale state issues
	c.StopMQTT()

	// Create MQTT bridge
	bridge := mqttbridge.NewMQTTBridge(config)
	bridge.SetLogger(c.logger)

	// Set initial device info if available
	c.mu.RLock()
	if c.selfInfo != nil {
		bridge.SetDeviceInfo(toDeviceInfo(c.selfInfo))
	}
	c.mu.RUnlock()

	// Set MQTT callbacks to forward events
	bridge.SetCallbacks(mqttbridge.MQTTCallbacks{
		OnConnected: func(broker string) {
			c.logger.Info("MQTT connected", "broker", broker)
		},
		OnDisconnected: func(broker string) {
			c.logger.Info("MQTT disconnected", "broker", broker)
		},
		OnStatus: func(status *mqttbridge.MQTTStatus) {
			c.logger.Debug("MQTT status received", "status", status.Status)
		},
		OnPacket: func(packet *mqttbridge.MQTTPacket) {
			c.logger.Debug("MQTT packet received", "direction", packet.Direction)
		},
		OnRaw: func(raw *mqttbridge.MQTTRaw) {
			c.logger.Debug("MQTT raw received")
		},
	})

	// Start the bridge
	if err := bridge.Start(); err != nil {
		return nil, fmt.Errorf("failed to start MQTT bridge: %w", err)
	}

	// Update client reference
	c.mu.Lock()
	c.mqtt = bridge
	c.mu.Unlock()

	// When self info is received, update MQTT device info
	originalOnSelfInfo := c.onSelfInfo
	c.onSelfInfo = func(info *SelfInfo) {
		bridge.SetDeviceInfo(toDeviceInfo(info))
		if originalOnSelfInfo != nil {
			originalOnSelfInfo(info)
		}
	}

	// When stats are received, update MQTT device stats
	originalOnStats := c.onStats
	c.onStats = func(stats *CompanionRadioStats) {
		bridge.SetDeviceStats(toDeviceStats(stats))
		if originalOnStats != nil {
			originalOnStats(stats)
		}
	}

	return bridge, nil
}

// StopMQTT stops the MQTT bridge and cleans up.
func (c *MeshCoreClient) StopMQTT() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mqtt != nil {
		c.mqtt.Stop()
		c.mqtt = nil
	}
}

// MQTTStatus returns a formatted string describing MQTT connection state.
func (c *MeshCoreClient) MQTTStatus() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mqtt == nil {
		return "mqtt: not configured"
	}
	return c.mqtt.FormatStatusReply()
}

// OnPacketReceived is called by the frame handler when a mesh packet is received.
// This forwards packets to MQTT for publishing if enabled.
func (c *MeshCoreClient) OnPacketReceived(rawData []byte, snr, rssi float64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mqtt != nil {
		c.mqtt.OnPacketReceived(rawData, snr, rssi)
	}
}

// OnPacketSent is called when a mesh packet is transmitted.
// This forwards packets to MQTT for publishing if enabled.
func (c *MeshCoreClient) OnPacketSent(rawData []byte) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mqtt != nil {
		c.mqtt.OnPacketSent(rawData)
	}
}

// MQTTConfig returns the current MQTT configuration.
func (c *MeshCoreClient) MQTTConfig() *mqttbridge.MQTTConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mqtt == nil {
		return nil
	}
	return c.mqtt.GetConfig()
}

// UpdateMQTTConfig updates the MQTT configuration at runtime.
func (c *MeshCoreClient) UpdateMQTTConfig(config *mqttbridge.MQTTConfig) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mqtt == nil {
		return fmt.Errorf("mqtt not configured")
	}
	c.mqtt.SetConfig(config)
	return nil
}

// GetMQTTConnectionState returns the connection state of all MQTT brokers.
func (c *MeshCoreClient) GetMQTTConnectionState() []mqttbridge.BrokerInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mqtt == nil {
		return nil
	}
	return c.mqtt.GetConnectionState()
}

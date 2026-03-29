package mqttbridge

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"filippo.io/edwards25519"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// regexp for sanitizing client IDs (MQTT spec: alphanumeric, dash, underscore, max 23 chars)
var clientIDRegex = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitizeClientID creates a valid MQTT client ID
func sanitizeClientID(id string) string {
	// Remove invalid characters
	clean := clientIDRegex.ReplaceAllString(id, "")
	// Truncate to 23 characters (MQTT spec maximum)
	if len(clean) > 23 {
		clean = clean[:23]
	}
	// Ensure minimum length
	if len(clean) < 1 {
		clean = "mc"
	}
	return clean
}

// Constants
const (
	// Default MQTT settings
	defaultKeepAlive        = 45
	defaultConnectTimeout   = 30
	defaultReconnectDelay   = 5
	maxQueuedPackets        = 10
	defaultStatusIntervalMs = 300000 // 5 minutes

	// Let's Mesh Analyzer servers
	analyzerUSURL = "wss://mqtt-us-v1.letsmesh.net:443/mqtt"
	analyzerEUURL = "wss://mqtt-eu-v1.letsmesh.net:443/mqtt"

	// Topic structure
	topicPattern = "meshcore/%s/%s/%s" // meshcore/{IATA}/{DEVICE_ID}/{type}
)

// GTS Root R4 Certificate for Let's Mesh Analyzer (Google Trust Services)
const gtsRootR4Cert = `-----BEGIN CERTIFICATE-----
MIIDejCCAmKgAwIBAgIQf+UwvzMTQ77dghYQST2KGzANBgkqhkiG9w0BAQsFADBX
MQswCQYDVQQGEwJCRTEZMBcGA1UEChMQR2xvYmFsU2lnbiBudi1zYTEQMA4GA1UE
CxMHUm9vdCBDQTEbMBkGA1UEAxMSR2xvYmFsU2lnbiBSb290IENBMB4XDTIzMTEx
NTAzNDMyMVoXDTI4MDEyODAwMDA0MlowRzELMAkGA1UEBhMCVVMxIjAgBgNVBAoT
GUdvb2dsZSBUcnVzdCBTZXJ2aWNlcyBMTEMxFDASBgNVBAMTC0dUUyBSb290IFI0
MHYwEAYHKoZIzj0CAQYFK4EEACIDYgAE83Rzp2iLYK5DuDXFgTB7S0md+8Fhzube
Rr1r1WEYNa5A3XP3iZEwWus87oV8okB2O6nGuEfYKueSkWpz6bFyOZ8pn6KY019e
WIZlD6GEZQbR3IvJx3PIjGov5cSr0R2Ko4H/MIH8MA4GA1UdDwEB/wQEAwIBhjAd
BgNVHSUEFjAUBggrBgEFBQcDAQYIKwYBBQUHAwIwDwYDVR0TAQH/BAUwAwEB/zAd
BgNVHQ4EFgQUgEzW63T/STaj1dj8tT7FavCUHYwwHwYDVR0jBBgwFoAUYHtmGkUN
l8qJUC99BM00qP/8/UswNgYIKwYBBQUHAQEEKjAoMCYGCCsGAQUFBzAChhpodHRw
Oi8vaS5wa2kuZ29vZy9nc3IxLmNydDAtBgNVHR8EJjAkMCKgIKAehhxodHRwOi8v
Yy5wa2kuZ29vZy9yL2dzcjEuY3JsMBMGA1UdIAQMMAowCAYGZ4EMAQIBMA0GCSqG
SIb3DQEBCwUAA4IBAQAYQrsPBtYDh5bjP2OBDwmkoWhIDDkic574y04tfzHpn+cJ
odI2D4SseesQ6bDrarZ7C30ddLibZatoKiws3UL9xnELz4ct92vID24FfVbiI1hY
+SW6FoVHkNeWIP0GCbaM4C6uVdF5dTUsMVs/ZbzNnIdCp5Gxmx5ejvEau8otR/Cs
kGN+hr/W5GvT1tMBjgWKZ1i4//emhA1JG1BbPzoLJQvyEotc03lXjTaCzv8mEbep
8RqZ7a2CPsgRbuvTPBwcOMBBmuFeU88+FSBX6+7iP0il8b4Z0QFqIwwMHfs/L6K1
vKAIiFDg0A4HnVWX+mYAMMLo1VKQaFfYwcfMFjPrPB37y0PBjBsHqWQ=
-----END CERTIFICATE-----`

// ============================================================================
// MQTT Bridge - Main Client Implementation
// ============================================================================

// MQTTBridge handles MQTT connectivity for MeshCore
type MQTTBridge struct {
	config     *MQTTConfig
	logger     *slog.Logger
	deviceInfo *DeviceInfo
	stats      *DeviceStats

	// MQTT clients
	customClient     mqtt.Client
	analyzerUSClient mqtt.Client
	analyzerEUClient mqtt.Client

	// Connection states
	customConnected     atomic.Bool
	analyzerUSConnected atomic.Bool
	analyzerEUConnected atomic.Bool

	// Reconnection state
	reconnectAttempts map[string]uint8
	reconnectDelay    map[string]time.Duration
	lastReconnect     map[string]time.Time

	// Packet queue
	queue   []QueuedPacket
	queueMu sync.Mutex

	// Status publishing
	lastStatusPublish time.Time
	statusInterval    time.Duration

	// JWT tokens for analyzer servers
	analyzerUsername string
	authTokenUS      string
	authTokenEU      string
	tokenExpiresAtUS time.Time
	tokenExpiresAtEU time.Time

	// Callbacks
	onStatus       func(*MQTTStatus)
	onPacket       func(*MQTTPacket)
	onRaw          func(*MQTTRaw)
	onConnected    func(string) // broker name
	onDisconnected func(string)

	// Control
	done chan struct{}
	mu   sync.RWMutex
	wg   sync.WaitGroup
}

// NewMQTTBridge creates a new MQTT bridge instance
func NewMQTTBridge(config *MQTTConfig) *MQTTBridge {
	if config == nil {
		config = DefaultMQTTConfig()
	}

	bridge := &MQTTBridge{
		config:            config,
		logger:            slog.Default(),
		reconnectAttempts: make(map[string]uint8),
		reconnectDelay:    make(map[string]time.Duration),
		lastReconnect:     make(map[string]time.Time),
		queue:             make([]QueuedPacket, 0, maxQueuedPackets),
		statusInterval:    time.Duration(config.StatusInterval) * time.Millisecond,
		done:              make(chan struct{}),
	}

	// Set default reconnect delays
	bridge.reconnectDelay["custom"] = 15 * time.Second
	bridge.reconnectDelay["us"] = 60 * time.Second
	bridge.reconnectDelay["eu"] = 60 * time.Second

	return bridge
}

// SetLogger sets a custom logger
func (b *MQTTBridge) SetLogger(logger *slog.Logger) {
	b.logger = logger
}

// SetDeviceInfo sets device info for MQTT topics
func (b *MQTTBridge) SetDeviceInfo(info *DeviceInfo) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deviceInfo = info

	// Generate analyzer username (format: v1_{UPPERCASE_PUBLIC_KEY})
	// CRITICAL: Broker expects uppercase hex, not lowercase!
	if info != nil && len(info.PublicKey) >= 32 {
		hexKey := strings.ToUpper(hex.EncodeToString(info.PublicKey))
		b.analyzerUsername = fmt.Sprintf("v1_%s", hexKey)
	}
}

// SetDeviceStats sets radio statistics for status messages
func (b *MQTTBridge) SetDeviceStats(stats *DeviceStats) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stats = stats
}

// SetCallbacks sets event callbacks
func (b *MQTTBridge) SetCallbacks(callbacks MQTTCallbacks) {
	b.onStatus = callbacks.OnStatus
	b.onPacket = callbacks.OnPacket
	b.onRaw = callbacks.OnRaw
	b.onConnected = callbacks.OnConnected
	b.onDisconnected = callbacks.OnDisconnected
}

// Start begins the MQTT connection process
func (b *MQTTBridge) Start() error {
	b.mu.Lock()
	if b.done == nil {
		b.done = make(chan struct{})
	}
	b.mu.Unlock()

	b.wg.Add(1)
	go b.connectionManager()

	return nil
}

// Stop gracefully stops the MQTT bridge
func (b *MQTTBridge) Stop() {
	b.mu.Lock()
	select {
	case <-b.done:
		b.mu.Unlock()
		return
	default:
	}
	close(b.done)
	b.mu.Unlock()

	// Disconnect all clients
	b.disconnectAll()

	b.wg.Wait()
	b.logger.Info("MQTT bridge stopped")
}

// connectionManager manages connections to all configured brokers
func (b *MQTTBridge) connectionManager() {
	defer b.wg.Done()

	b.logger.Info("Starting MQTT connection manager")

	// Initial connection delay
	select {
	case <-b.done:
		return
	case <-time.After(2 * time.Second):
	}

	for {
		select {
		case <-b.done:
			return
		default:
		}

		// Connect to custom broker if configured
		if b.isCustomBrokerConfigured() {
			b.connectToCustomBroker()
		}

		// Connect to Let's Mesh Analyzer servers
		b.mu.RLock()
		ownerEmail := b.config.OwnerEmail
		b.mu.RUnlock()

		if ownerEmail != "" {
			if b.config.AnalyzerUS {
				b.connectToAnalyzer("us", analyzerUSURL)
			}
			if b.config.AnalyzerEU {
				b.connectToAnalyzer("eu", analyzerEUURL)
			}
		} else {
			// Log once per connection cycle to avoid spam
			if b.analyzerUSConnected.Load() == false && b.analyzerEUConnected.Load() == false {
				b.logger.Debug("Skipping Let's Mesh Analyzer: OwnerEmail not configured")
			}
		}

		// Process packet queue periodically
		b.processQueue()

		// Publish status periodically
		b.publishStatusIfNeeded()

		// Wait before next connection attempt cycle
		select {
		case <-b.done:
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// isCustomBrokerConfigured checks if custom MQTT broker is configured
func (b *MQTTBridge) isCustomBrokerConfigured() bool {
	return b.config.Server != "" && b.config.Port > 0
}

// connectToCustomBroker establishes connection to custom MQTT broker
func (b *MQTTBridge) connectToCustomBroker() {
	if b.customConnected.Load() {
		return
	}

	// Check reconnect delay
	if !b.canReconnect("custom") {
		return
	}

	// Disconnect and discard any existing client before creating a new one.
	// This prevents two clients with the same ID competing on the broker,
	// which would trigger an infinite kick-reconnect loop.
	if b.customClient != nil {
		b.customClient.Disconnect(250)
		b.customClient = nil
	}

	b.logger.Info("Connecting to custom MQTT broker",
		"server", b.config.Server,
		"port", b.config.Port)

	brokerURL := b.buildBrokerURL(b.config.Server, b.config.Port, b.config.UseTLS)

	// autoReconnect=false: connectionManager owns all reconnection for the TCP
	// broker. Paho auto-reconnect must be off to prevent it from internally
	// queuing QoS-1 messages and "completing" their tokens without actual PUBACK
	// from the broker, which caused silent publish failures on every 2nd+ update.
	client, err := b.createMQTTClient("custom", brokerURL,
		b.config.Username, b.config.Password,
		b.config.UseTLS, "", "", "", false)

	if err != nil {
		b.logger.Error("Failed to create custom MQTT client", "error", err)
		b.scheduleReconnect("custom")
		return
	}

	b.customClient = client
	// Record the connection attempt time so that connectionManager does not
	// immediately spin up another client on the next 5-second tick if the
	// OnConnect callback hasn't fired yet.
	b.lastReconnect["custom"] = time.Now()
}

// connectToAnalyzer establishes connection to Let's Mesh Analyzer server
func (b *MQTTBridge) connectToAnalyzer(region, brokerURL string) {
	switch region {
	case "us":
		if b.analyzerUSConnected.Load() {
			return
		}
	case "eu":
		if b.analyzerEUConnected.Load() {
			return
		}
	default:
		return
	}

	// Check reconnect delay
	if !b.canReconnect(region) {
		return
	}

	// Disconnect and discard any existing client before creating a new one.
	// This prevents two clients with the same ID competing on the broker,
	// which would trigger an infinite kick-reconnect loop (same fix as custom broker).
	switch region {
	case "us":
		if b.analyzerUSClient != nil {
			b.analyzerUSClient.Disconnect(250)
			b.analyzerUSClient = nil
		}
	case "eu":
		if b.analyzerEUClient != nil {
			b.analyzerEUClient.Disconnect(250)
			b.analyzerEUClient = nil
		}
	}

	b.logger.Info("Connecting to Let's Mesh Analyzer", "region", region, "url", brokerURL)

	// For analyzer servers, use JWT token as password
	password := b.getAuthToken(region)
	if password == "" {
		b.logger.Warn("No auth token available for analyzer", "region", region)
		b.scheduleReconnect(region)
		return
	}

	// Client ID must be unique per device; use region + first 8 chars of public key.
	// Using a fixed ID like "mc-us" would cause the broker to kick the previous
	// connection every time any other device using this software connects.
	clientID := region
	if b.analyzerUsername != "" {
		// analyzerUsername is "v1_{PUBKEY}" — take 8 chars of pubkey for uniqueness
		parts := strings.SplitN(b.analyzerUsername, "_", 2)
		if len(parts) == 2 && len(parts[1]) >= 8 {
			clientID = fmt.Sprintf("%s-%s", region, strings.ToLower(parts[1][:8]))
		}
	}

	var newClient mqtt.Client
	newClient, err := b.createMQTTClient(clientID, brokerURL,
		b.analyzerUsername, password,
		true, gtsRootR4Cert, "", "", true) // Always use TLS for analyzer; paho reconnect OK for WS

	if err != nil {
		b.logger.Error("Failed to create analyzer MQTT client", "error", err, "region", region)
		b.scheduleReconnect(region)
		return
	}

	// Record the connection attempt time so connectionManager does not immediately
	// spin up another client on the next 5-second tick before OnConnect fires.
	b.lastReconnect[region] = time.Now()

	switch region {
	case "us":
		b.analyzerUSClient = newClient
	case "eu":
		b.analyzerEUClient = newClient
	}
}

// buildBrokerURL constructs the broker URL from components
func (b *MQTTBridge) buildBrokerURL(host string, port int, useTLS bool) string {
	scheme := "tcp"
	if useTLS {
		scheme = "ssl"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}

// createMQTTClient creates and configures an MQTT client.
// autoReconnect controls whether paho handles reconnection internally.
// For the custom TCP broker, pass false and let connectionManager own reconnects.
// For WebSocket analyzer brokers, pass true (connections are more stable).
func (b *MQTTBridge) createMQTTClient(clientID, brokerURL, username, password string,
	useTLS bool, caCert, clientCert, clientKey string, autoReconnect bool) (mqtt.Client, error) {

	// Generate a clean client ID (alphanumeric, max 23 chars for MQTT spec)
	cleanID := sanitizeClientID(fmt.Sprintf("mc-%s", clientID))

	opts := mqtt.NewClientOptions()
	opts.AddBroker(brokerURL)
	opts.SetClientID(cleanID)
	opts.SetUsername(username)
	opts.SetPassword(password)
	opts.SetKeepAlive(defaultKeepAlive * time.Second)
	opts.SetConnectTimeout(defaultConnectTimeout * time.Second)
	opts.SetPingTimeout(10 * time.Second)  // detect missing PINGRESP quickly
	opts.SetWriteTimeout(20 * time.Second) // unblock writes on dead TCP sockets
	opts.SetAutoReconnect(autoReconnect)
	if autoReconnect {
		opts.SetReconnectingHandler(func(c mqtt.Client, opts *mqtt.ClientOptions) {
			b.logger.Info("MQTT client reconnecting", "broker", brokerURL)
		})
	}

	// Configure TLS if needed
	if useTLS && caCert != "" {
		tlsConfig := &tls.Config{}
		if caCert != "" {
			certPool := x509.NewCertPool()
			if certPool.AppendCertsFromPEM([]byte(caCert)) {
				tlsConfig.RootCAs = certPool
			}
		}
		if clientCert != "" && clientKey != "" {
			cert, err := tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
			if err == nil {
				tlsConfig.Certificates = []tls.Certificate{cert}
			}
		}
		opts.SetTLSConfig(tlsConfig)
	}

	// Connection handlers
	opts.OnConnect = func(c mqtt.Client) {
		b.handleConnect(clientID)
	}
	opts.OnConnectionLost = func(c mqtt.Client, err error) {
		b.handleConnectionLost(clientID, err)
	}

	client := mqtt.NewClient(opts)
	token := client.Connect()

	if token.Wait() && token.Error() != nil {
		return nil, token.Error()
	}

	return client, nil
}

// handleConnect handles successful MQTT connection
func (b *MQTTBridge) handleConnect(brokerID string) {
	b.logger.Debug("MQTT connected in handle", "broker", brokerID)

	var connected *atomic.Bool
	switch brokerID {
	case "custom":
		connected = &b.customConnected
	case "us":
		connected = &b.analyzerUSConnected
	case "eu":
		connected = &b.analyzerEUConnected
	default:
		return
	}

	connected.Store(true)
	b.reconnectAttempts[brokerID] = 0
	b.reconnectDelay[brokerID] = 15 * time.Second

	if b.onConnected != nil {
		b.onConnected(brokerID)
	}
}

// handleConnectionLost handles connection loss
func (b *MQTTBridge) handleConnectionLost(brokerID string, err error) {
	b.logger.Warn("MQTT connection lost", "broker", brokerID, "error", err)

	var connected *atomic.Bool
	switch brokerID {
	case "custom":
		connected = &b.customConnected
	case "us":
		connected = &b.analyzerUSConnected
	case "eu":
		connected = &b.analyzerEUConnected
	default:
		return
	}

	connected.Store(false)

	if b.onDisconnected != nil {
		b.onDisconnected(brokerID)
	}
}

// handleDisconnecting handles disconnection
func (b *MQTTBridge) handleDisconnecting(brokerID string) {
	b.logger.Info("MQTT disconnecting", "broker", brokerID)
}

// canReconnect checks if enough time has passed to attempt reconnection
func (b *MQTTBridge) canReconnect(brokerID string) bool {
	delay := b.reconnectDelay[brokerID]
	lastTime := b.lastReconnect[brokerID]

	if delay == 0 {
		delay = 5 * time.Second
	}

	if !lastTime.IsZero() && time.Since(lastTime) < delay {
		return false
	}

	return true
}

// scheduleReconnect schedules a reconnection attempt with exponential backoff
func (b *MQTTBridge) scheduleReconnect(brokerID string) {
	b.lastReconnect[brokerID] = time.Now()

	// Exponential backoff: 15s, 30s, 60s, 120s, 300s
	attempts := b.reconnectAttempts[brokerID]
	if attempts < 5 {
		b.reconnectAttempts[brokerID] = attempts + 1
	}

	var delay time.Duration
	switch b.reconnectAttempts[brokerID] {
	case 0:
		delay = 15 * time.Second
	case 1:
		delay = 30 * time.Second
	case 2:
		delay = 60 * time.Second
	case 3:
		delay = 120 * time.Second
	default:
		delay = 300 * time.Second
	}

	b.reconnectDelay[brokerID] = delay
	b.logger.Info("Scheduled reconnect", "broker", brokerID, "delay", delay)
}

// resetCustomConnection tears down the custom broker client and clears all
// related state so that connectionManager will immediately attempt to reconnect
// on the next 5-second tick. Call this whenever a publish fails or times out.
func (b *MQTTBridge) resetCustomConnection() {
	b.customConnected.Store(false)
	// Zero out lastReconnect so canReconnect() allows an immediate retry.
	b.lastReconnect["custom"] = time.Time{}
	if b.customClient != nil {
		old := b.customClient
		b.customClient = nil
		// Best-effort graceful disconnect; don't block the caller.
		go old.Disconnect(250)
	}
	b.logger.Warn("Custom broker connection reset; will reconnect on next tick")
}

// disconnectAll disconnects all MQTT clients
func (b *MQTTBridge) disconnectAll() {
	if b.customClient != nil && b.customClient.IsConnected() {
		b.customClient.Disconnect(1000)
	}
	if b.analyzerUSClient != nil && b.analyzerUSClient.IsConnected() {
		b.analyzerUSClient.Disconnect(1000)
	}
	if b.analyzerEUClient != nil && b.analyzerEUClient.IsConnected() {
		b.analyzerEUClient.Disconnect(1000)
	}

	b.customConnected.Store(false)
	b.analyzerUSConnected.Store(false)
	b.analyzerEUConnected.Store(false)
}

// ============================================================================
// Packet Publishing
// ============================================================================

// QueuePacket adds a packet to the queue for publishing
func (b *MQTTBridge) QueuePacket(rawData []byte, isTX bool, snr, rssi float64) {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()

	// Check queue size limit
	if len(b.queue) >= maxQueuedPackets {
		// Drop oldest packet
		b.queue = b.queue[1:]
		b.logger.Warn("Packet queue full, dropping oldest")
	}

	// Calculate packet hash for deduplication
	hash := fmt.Sprintf("%x", hashData(rawData))

	packet := QueuedPacket{
		RawData:   rawData,
		Timestamp: time.Now(),
		IsTX:      isTX,
		SNR:       snr,
		RSSI:      rssi,
		Hash:      hash,
	}

	b.queue = append(b.queue, packet)
	b.logger.Debug("Packet queued", "hash", hash, "queue_len", len(b.queue))
}

// QueueChannelPacket queues a raw RF packet that has been correlated with a
// decoded BLE channel message frame. The decoded fields (channel name, sender
// name, message text) are embedded in the published JSON so the web UI can
// display them without needing to parse or decrypt the raw bytes.
func (b *MQTTBridge) QueueChannelPacket(rawData []byte, isTX bool, snr, rssi float64, channelIndex int, channelName, senderName, text, channelKeyHex string) {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()

	if len(b.queue) >= maxQueuedPackets {
		b.queue = b.queue[1:]
		b.logger.Warn("Packet queue full, dropping oldest")
	}

	hash := fmt.Sprintf("%x", hashData(rawData))
	packet := QueuedPacket{
		RawData:          rawData,
		Timestamp:        time.Now(),
		IsTX:             isTX,
		SNR:              snr,
		RSSI:             rssi,
		Hash:             hash,
		IsChannelMessage: true,
		ChannelIndex:     channelIndex,
		ChannelName:      channelName,
		SenderName:       senderName,
		Text:             text,
		ChannelKey:       channelKeyHex,
	}
	b.queue = append(b.queue, packet)
	b.logger.Debug("Channel packet queued", "channel", channelIndex, "sender", senderName, "isTX", isTX, "queue_len", len(b.queue))
}

// hashData computes a simple hash of data for deduplication
func hashData(data []byte) uint64 {
	var hash uint64 = 5381
	for _, b := range data {
		hash = ((hash << 5) + hash) + uint64(b)
	}
	return hash
}

// processQueue processes queued packets for publishing
func (b *MQTTBridge) processQueue() {
	b.queueMu.Lock()
	if len(b.queue) == 0 {
		b.queueMu.Unlock()
		return
	}

	// Check if any broker is connected
	if !b.isAnyConnected() {
		b.queueMu.Unlock()
		return
	}

	// Process one packet at a time for responsiveness
	packet := b.queue[0]
	b.queue = b.queue[1:]
	b.queueMu.Unlock()

	b.publishPacket(&packet)
}

// publishPacket publishes a single packet to all connected brokers
func (b *MQTTBridge) publishPacket(packet *QueuedPacket) {
	b.mu.RLock()
	deviceInfo := b.deviceInfo
	b.mu.RUnlock()

	if deviceInfo == nil {
		b.logger.Warn("Cannot publish packet: no device info")
		return
	}

	originID := hex.EncodeToString(deviceInfo.PublicKey)
	origin := b.config.Origin
	if origin == "" {
		origin = deviceInfo.Name
	}

	// Build packet message
	mqttPacket := &MQTTPacket{
		Origin:     origin,
		OriginID:   originID,
		Timestamp:  packet.Timestamp.UTC().Format("2006-01-02T15:04:05.000000"),
		Type:       "PACKET",
		Direction:  directionStr(packet.IsTX),
		Time:       packet.Timestamp.UTC().Format("15:04:05"),
		Date:       packet.Timestamp.UTC().Format("02/01/2006"),
		Len:        fmt.Sprintf("%d", len(packet.RawData)),
		PacketType: fmt.Sprintf("%d", getPayloadType(packet.RawData)),
		Route:      getRouteType(packet.RawData),
		PayloadLen: fmt.Sprintf("%d", getPayloadLen(packet.RawData)),
		Raw:        hex.EncodeToString(packet.RawData),
		SNR:        fmt.Sprintf("%.1f", packet.SNR),
		RSSI:       fmt.Sprintf("%.1f", packet.RSSI),
		Hash:       packet.Hash,
	}

	// Attach decoded channel message fields when available so the web UI can
	// display channel name, sender, and message text without parsing raw bytes.
	if packet.IsChannelMessage {
		idx := packet.ChannelIndex
		mqttPacket.ChannelIndex = &idx
		mqttPacket.ChannelName = packet.ChannelName
		mqttPacket.SenderName = packet.SenderName
		mqttPacket.Text = packet.Text
		mqttPacket.ChannelKey = packet.ChannelKey
	}

	// Build topics
	iata := b.config.IATA
	if iata == "" {
		iata = "XXX"
	}

	topic := fmt.Sprintf(topicPattern, iata, originID, "packets")
	jsonData, err := json.Marshal(mqttPacket)
	if err != nil {
		b.logger.Error("Failed to marshal packet", "error", err)
		return
	}

	// Publish to custom broker
	if b.customConnected.Load() && b.customClient != nil {
		if !b.customClient.IsConnected() {
			b.logger.Warn("Custom broker reports not connected during packet publish, resetting")
			b.resetCustomConnection()
		} else {
			token := b.customClient.Publish(topic, 1, false, jsonData)
			if !token.WaitTimeout(15 * time.Second) {
				b.logger.Warn("Packet publish to custom broker timed out, resetting connection")
				b.resetCustomConnection()
			} else if token.Error() != nil {
				b.logger.Error("Failed to publish packet to custom broker", "error", token.Error())
				b.resetCustomConnection()
			}
		}
	}

	// Publish to analyzer servers
	b.publishToAnalyzers(topic, jsonData)

	// Publish raw if enabled
	if b.config.RawEnabled {
		b.publishRawPacket(packet, originID, origin, iata)
	}
}

// publishRawPacket publishes a raw packet message
func (b *MQTTBridge) publishRawPacket(packet *QueuedPacket, originID, origin, iata string) {
	rawMsg := &MQTTRaw{
		Origin:    origin,
		OriginID:  originID,
		Timestamp: packet.Timestamp.UTC().Format("2006-01-02T15:04:05.000000"),
		Type:      "RAW",
		Data:      hex.EncodeToString(packet.RawData),
	}

	topic := fmt.Sprintf(topicPattern, iata, originID, "raw")
	jsonData, err := json.Marshal(rawMsg)
	if err != nil {
		b.logger.Error("Failed to marshal raw packet", "error", err)
		return
	}

	b.publishToAnalyzers(topic, jsonData)
}

// publishToAnalyzers publishes data to Let's Mesh Analyzer servers.
// Uses IsConnected() as the primary truth source (same pattern as publishStatus)
// to avoid silent drops when the connected atomic flag lags behind paho state.
// WaitTimeout prevents blocking forever when connection drops mid-publish
// (paho autoReconnect would otherwise keep the token pending across reconnects).
func (b *MQTTBridge) publishToAnalyzers(topic string, data []byte) {
	const analyzerPublishTimeout = 15 * time.Second

	if b.analyzerUSClient != nil && b.analyzerUSClient.IsConnected() {
		if !b.analyzerUSConnected.Load() {
			b.analyzerUSConnected.Store(true)
		}
		token := b.analyzerUSClient.Publish(topic, 1, false, data)
		if !token.WaitTimeout(analyzerPublishTimeout) {
			b.logger.Warn("Publish to US analyzer timed out", "topic", topic)
		} else if token.Error() != nil {
			b.logger.Error("Failed to publish to US analyzer", "error", token.Error(), "topic", topic)
		}
	}

	if b.analyzerEUClient != nil && b.analyzerEUClient.IsConnected() {
		if !b.analyzerEUConnected.Load() {
			b.analyzerEUConnected.Store(true)
		}
		token := b.analyzerEUClient.Publish(topic, 1, false, data)
		if !token.WaitTimeout(analyzerPublishTimeout) {
			b.logger.Warn("Publish to EU analyzer timed out", "topic", topic)
		} else if token.Error() != nil {
			b.logger.Error("Failed to publish to EU analyzer", "error", token.Error(), "topic", topic)
		}
	}
}

// directionStr converts boolean to direction string
func directionStr(isTX bool) string {
	if isTX {
		return "tx"
	}
	return "rx"
}

// MeshCore packet header byte layout (from Packet.h):
//
//	bits [1:0] = route type  (PH_ROUTE_MASK = 0x03)
//	bits [5:2] = payload type (PH_TYPE_MASK = 0x0F, shifted by PH_TYPE_SHIFT = 2)
//	bits [7:6] = payload version
//
// Route type constants:
//
//	0x00 ROUTE_TYPE_TRANSPORT_FLOOD
//	0x01 ROUTE_TYPE_FLOOD
//	0x02 ROUTE_TYPE_DIRECT
//	0x03 ROUTE_TYPE_TRANSPORT_DIRECT
//
// Payload type constants (relevant subset):
//
//	0x02 PAYLOAD_TYPE_TXT_MSG   – plain text DM
//	0x04 PAYLOAD_TYPE_ADVERT    – node advertisement
//	0x05 PAYLOAD_TYPE_GRP_TXT   – group/channel text message

// getPayloadType extracts the payload type from the header byte.
func getPayloadType(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	return int((data[0] >> 2) & 0x0F)
}

// getRouteType returns a single-character route label from the header byte.
func getRouteType(data []byte) string {
	if len(data) == 0 {
		return "U"
	}
	switch data[0] & 0x03 {
	case 0x01:
		return "F" // ROUTE_TYPE_FLOOD
	case 0x02:
		return "D" // ROUTE_TYPE_DIRECT
	case 0x00, 0x03:
		return "T" // ROUTE_TYPE_TRANSPORT_FLOOD / ROUTE_TYPE_TRANSPORT_DIRECT
	default:
		return "U"
	}
}

// getPayloadLen returns the payload byte count from the raw packet.
//
// Wire format (from Packet.cpp writeTo()):
//
//	[header(1)] [transport_codes(4) — only for TRANSPORT routes] [path_len(1)] [path...] [payload...]
//
// path_len byte encoding is identical for ALL route types (from Packet.h):
//
//	hash_count = path_len & 0x3F   (bits 0-5)
//	hash_size  = (path_len >> 6) + 1  (bits 6-7 → 1/2/3 bytes per hop; 4 is reserved/invalid)
//	path bytes = hash_count * hash_size
func getPayloadLen(data []byte) int {
	if len(data) < 2 {
		return 0
	}
	routeType := data[0] & 0x03
	hasTransportCodes := routeType == 0x00 || routeType == 0x03 // TRANSPORT_FLOOD or TRANSPORT_DIRECT

	// header(1) + optional transport_codes(4) + path_len(1)
	headerLen := 2
	if hasTransportCodes {
		headerLen = 6
	}
	if len(data) < headerLen {
		return 0
	}

	pathLenByte := data[headerLen-1]
	hashCount := int(pathLenByte & 0x3F)
	hashSize := int((pathLenByte>>6)&0x03) + 1 // 1, 2, or 3 bytes per hop
	pathBytes := hashCount * hashSize

	offset := headerLen + pathBytes
	if offset >= len(data) {
		return 0
	}
	return len(data) - offset
}

// ============================================================================
// Generic Publish Helper
// ============================================================================

// publishToAllBrokers publishes data to all connected MQTT brokers.
// retained: true for persistent data (status), false for transient data (packets).
func (b *MQTTBridge) publishToAllBrokers(topic string, data []byte, retained bool) {
	// Log the intent
	b.logger.Debug("publishToAllBrokers",
		"topic", topic,
		"data_len", len(data),
		"retained", retained,
		"custom_connected_flag", b.customConnected.Load(),
		"custom_client_nil", b.customClient == nil,
		"any_connected", b.isAnyConnected())

	// Publish to custom broker
	if b.customClient != nil && b.customClient.IsConnected() {
		// Ensure atomic flag reflects actual connection state
		if !b.customConnected.Load() {
			b.logger.Debug("publishToAllBrokers: flag out-of-sync, updating")
			b.customConnected.Store(true)
		}
		token := b.customClient.Publish(topic, 1, retained, data)
		b.logger.Debug("publishToAllBrokers: custom broker publish started",
			"topic", topic,
			"qos", 1,
			"retained", retained,
			"timeout", "15s")
		if !token.WaitTimeout(15 * time.Second) {
			b.logger.Error("publishToAllBrokers: custom broker publish timed out (no PUBACK)",
				"topic", topic)
			b.resetCustomConnection()
		} else if token.Error() != nil {
			b.logger.Error("publishToAllBrokers: custom broker publish failed",
				"topic", topic,
				"error", token.Error())
			b.resetCustomConnection()
		} else {
			b.logger.Info("Published to custom broker", "topic", topic)
		}
	} else {
		b.logger.Warn("publishToAllBrokers: custom broker not ready, message dropped",
			"topic", topic,
			"custom_client_nil", b.customClient == nil)
	}

	// Publish to analyzer servers
	b.publishToAnalyzers(topic, data)
}

// ============================================================================
// Status Publishing
// ============================================================================

// publishStatusIfNeeded publishes status message if interval has passed
func (b *MQTTBridge) publishStatusIfNeeded() {
	if !b.config.StatusEnabled {
		b.logger.Debug("Status publish skipped: StatusEnabled is false")
		return
	}

	// Check if any broker is connected
	if !b.isAnyConnected() {
		b.logger.Debug("Status publish skipped: no broker connected",
			"custom", b.customConnected.Load(),
			"analyzer_us", b.analyzerUSConnected.Load(),
			"analyzer_eu", b.analyzerEUConnected.Load())
		return
	}

	now := time.Now()
	elapsed := time.Duration(0)
	if !b.lastStatusPublish.IsZero() {
		elapsed = now.Sub(b.lastStatusPublish)
	}

	if !b.lastStatusPublish.IsZero() && elapsed < b.statusInterval {
		b.logger.Debug("Status publish skipped: interval not reached",
			"elapsed", elapsed, "interval", b.statusInterval)
		return
	}

	b.logger.Debug("Status publish triggered",
		"last_publish", b.lastStatusPublish,
		"elapsed", elapsed,
		"interval", b.statusInterval,
		"custom_connected", b.customConnected.Load())
	b.publishStatus()
}

// publishStatus publishes a status message to all connected brokers.
// The message is marked as retained so newly connected subscribers receive
// the latest state immediately.
func (b *MQTTBridge) publishStatus() {
	b.mu.RLock()
	deviceInfo := b.deviceInfo
	stats := b.stats
	config := b.config
	b.mu.RUnlock()

	if deviceInfo == nil {
		b.logger.Warn("Cannot publish status: no device info")
		return
	}

	b.logger.Debug("PUBLISH_STATUS: Starting status publish",
		"status_enabled", config.StatusEnabled,
		"custom_connected", b.customConnected.Load(),
		"analyzer_us_connected", b.analyzerUSConnected.Load(),
		"analyzer_eu_connected", b.analyzerEUConnected.Load(),
		"device_info_nil", deviceInfo == nil,
		"stats_nil", stats == nil)

	// Log stats details if available
	if stats != nil {
		b.logger.Debug("PUBLISH_STATUS: Stats data",
			"uptime_secs", stats.UptimeSecs,
			"packets_recv", stats.PacketsRecv,
			"packets_sent", stats.PacketsSent,
			"tx_air_secs", stats.TxAirSecs,
			"rx_air_secs", stats.RxAirSecs,
			"last_rssi", stats.LastRssi,
			"last_snr", stats.LastSnrDb,
			"battery_mv", stats.BatteryMv,
			"queue_len", stats.QueueLen,
			"errors", stats.Errors)
	} else {
		b.logger.Warn("PUBLISH_STATUS: No stats available")
	}

	originID := hex.EncodeToString(deviceInfo.PublicKey)
	origin := b.config.Origin
	if origin == "" {
		origin = deviceInfo.Name
	}

	// Build status message with all available device parameters.
	mqttStatus := &MQTTStatus{
		Status:          "online",
		Timestamp:       time.Now().UTC().Format("2006-01-02T15:04:05.000000"),
		Origin:          origin,
		OriginID:        originID,
		Model:           "MeshCore",
		FirmwareVersion: "v1.0.0",
		Radio:           buildRadioString(deviceInfo),
		ClientVersion:   "meshcore/v1.0.0",
	}

	// GPS coordinates — include only when the device has a valid fix.
	// In MeshCore, (0,0) means "no GPS" (it is in the ocean and unreachable by LoRa).
	if deviceInfo.Latitude != 0 || deviceInfo.Longitude != 0 {
		mqttStatus.Lat = deviceInfo.Latitude
		mqttStatus.Lon = deviceInfo.Longitude
	}

	// TX power — include when non-zero (0 dBm is an invalid TX power for any
	// real LoRa device; the field is simply unset in that case).
	if deviceInfo.TxPower != 0 {
		mqttStatus.TxPower = int(deviceInfo.TxPower)
	}

	// Comprehensive stats — all counters from the last CMD_GET_STATS response.
	if stats != nil {
		mqttStatus.Stats = buildMQTTStats(stats)
	}

	// Build topic and serialize.
	iata := b.config.IATA
	if iata == "" {
		iata = "XXX"
	}

	topic := fmt.Sprintf(topicPattern, iata, originID, "status")
	jsonData, err := json.Marshal(mqttStatus)
	if err != nil {
		b.logger.Error("Failed to marshal status", "error", err)
		return
	}

	b.logger.Debug("PUBLISH_STATUS: Publishing to custom broker",
		"topic", topic,
		"json_size", len(jsonData),
		"json_preview", string(jsonData[:min(len(jsonData), 200)]),
		"qos", 1,
		"retained", true,
		"custom_connected_flag", b.customConnected.Load(),
		"custom_client_nil", b.customClient == nil)

	// Publish to custom broker (retained).
	// Guard with paho's own IsConnected() to catch stale customConnected flags,
	// then use WaitTimeout so a dead connection never blocks the goroutine forever.
	if b.customClient != nil && b.customClient.IsConnected() {
		if !b.customConnected.Load() {
			b.logger.Warn("PUBLISH_STATUS: paho connected but flag false, updating flag")
			b.customConnected.Store(true)
		}

		b.logger.Debug("PUBLISH_STATUS: Calling paho Publish")
		token := b.customClient.Publish(topic, 1, true, jsonData)

		b.logger.Debug("PUBLISH_STATUS: Waiting for publish token")
		if !token.WaitTimeout(15 * time.Second) {
			b.logger.Error("PUBLISH_STATUS: Publish to custom broker timed out (no PUBACK)",
				"topic", topic,
				"token_waited", true)
			b.resetCustomConnection()
		} else if token.Error() != nil {
			b.logger.Error("PUBLISH_STATUS: Failed to publish status to custom broker",
				"error", token.Error(),
				"topic", topic)
			b.resetCustomConnection()
		} else {
			b.logger.Debug("PUBLISH_STATUS: Successfully published to custom broker",
				"topic", topic,
				"json_size", len(jsonData))
		}
	} else {
		customConnected := b.customClient != nil && b.customClient.IsConnected()
		b.logger.Warn("PUBLISH_STATUS: Cannot publish - custom broker not connected",
			"custom_connected_flag", b.customConnected.Load(),
			"custom_client_nil", b.customClient == nil,
			"custom_client_connected", customConnected)
	}

	// Publish to analyzer servers (retained).
	b.publishStatusToAnalyzers(topic, jsonData)

	b.lastStatusPublish = time.Now()
	b.logger.Debug("Status published", "topic", topic,
		"lat", mqttStatus.Lat, "lon", mqttStatus.Lon,
		"tx_power", mqttStatus.TxPower,
		"has_stats", mqttStatus.Stats != nil)

	b.logger.Info("Status published")
}

// buildRadioString constructs the radio-config string from DeviceInfo.
//
// Format expected by Let's Mesh Analyzer: "{freq_khz},{bw_khz},{sf},{cr}"
//   - freq_khz — carrier frequency in kHz  (e.g. 869525.000)
//   - bw_khz   — channel bandwidth in kHz  (e.g. 62.5, 125.0, 250.0, 500.0)
//   - sf        — LoRa spreading factor     (7–12)
//   - cr        — LoRa coding rate          (5=4/5 … 8=4/8)
func buildRadioString(info *DeviceInfo) string {
	if info == nil || info.FreqHz == 0 {
		return ""
	}
	freqKHz := float64(info.FreqHz) / 1e3
	bwKHz := float64(info.BwHz) / 1e3
	return fmt.Sprintf("%.3f,%.1f,%d,%d", freqKHz, bwKHz, info.Sf, info.Cr)
}

// buildMQTTStats maps all fields from DeviceStats → MQTTStats.
//
// Airtime utilization is derived as:
//
//	(TxAirSecs + RxAirSecs) / UptimeSecs × 100   [rounded to 2 decimal places]
//
// Fields whose source value is naturally zero when not-yet-populated (e.g.
// BatteryMv=0 means no battery telemetry) are left at their Go zero value and
// therefore omitted from the JSON output by the `omitempty` tag.
func buildMQTTStats(stats *DeviceStats) *MQTTStats {
	if stats == nil {
		return nil
	}

	s := &MQTTStats{
		// Core
		BatteryMV:  int(stats.BatteryMv),
		UptimeSecs: int(stats.UptimeSecs),
		Errors:     int(stats.Errors),
		QueueLen:   int(stats.QueueLen),

		// Radio channel
		NoiseFloor: int(stats.NoiseFloorDbm),
		TxAirSecs:  int(stats.TxAirSecs),
		RxAirSecs:  int(stats.RxAirSecs),
		LastRssi:   int(stats.LastRssi),
		LastSnr:    math.Round(float64(stats.LastSnrDb)*100) / 100, // 2 dp

		// Packet counters
		PacketsRecv:     int(stats.PacketsRecv),
		PacketsSent:     int(stats.PacketsSent),
		PacketsFloodTx:  int(stats.PacketsFloodTx),
		PacketsDirectTx: int(stats.PacketsDirectTx),
		PacketsFloodRx:  int(stats.PacketsFloodRx),
		PacketsDirectRx: int(stats.PacketsDirectRx),
		RecvErrors:      int(stats.PacketsRecvErrors),
	}

	// Airtime utilization percentage — only meaningful once the device has been
	// up for at least one second and has logged some air time.
	if stats.UptimeSecs > 0 {
		airSecs := float64(stats.TxAirSecs + stats.RxAirSecs)
		s.AirtimeUtilPct = math.Round(airSecs/float64(stats.UptimeSecs)*10000) / 100
	}

	return s
}

// publishStatusToAnalyzers publishes status to Let's Mesh Analyzer servers
func (b *MQTTBridge) publishStatusToAnalyzers(topic string, data []byte) {
	if b.analyzerUSConnected.Load() && b.analyzerUSClient != nil {
		token := b.analyzerUSClient.Publish(topic, 1, true, data)
		token.Wait()
		if token.Error() != nil {
			b.logger.Error("Failed to publish status to US analyzer", "error", token.Error())
		}
		b.logger.Debug("Status published", "broker", "US analyzer")
	}

	if b.analyzerEUConnected.Load() && b.analyzerEUClient != nil {
		token := b.analyzerEUClient.Publish(topic, 1, true, data)
		token.Wait()
		if token.Error() != nil {
			b.logger.Error("Failed to publish status to EU analyzer", "error", token.Error())
		}
		b.logger.Debug("Status published", "broker", "EU analyzer")

	}
}

// ============================================================================
// Authentication
// ============================================================================

// getAuthToken returns the current auth token for analyzer servers
func (b *MQTTBridge) getAuthToken(region string) string {
	// Check if token needs renewal
	var expiresAt time.Time
	switch region {
	case "us":
		expiresAt = b.tokenExpiresAtUS
	case "eu":
		expiresAt = b.tokenExpiresAtEU
	}

	// Renew token if expiring soon
	if time.Until(expiresAt) < 5*time.Minute {
		b.renewAuthToken(region)
	}

	switch region {
	case "us":
		return b.authTokenUS
	case "eu":
		return b.authTokenEU
	}
	return ""
}

// renewAuthToken renews the JWT token for analyzer servers
func (b *MQTTBridge) renewAuthToken(region string) {
	b.mu.RLock()
	deviceInfo := b.deviceInfo
	ownerEmail := b.config.OwnerEmail
	devicePrivKey := b.config.DevicePrivKey
	b.mu.RUnlock()

	// Require owner email for Let's Mesh Analyzer authentication
	if ownerEmail == "" {
		b.logger.Warn("Cannot renew auth token: owner_email not configured for Let's Mesh Analyzer",
			"region", region, "hint", "Set OwnerEmail in MQTTConfig to enable analyzer servers")
		return
	}

	if deviceInfo == nil || len(deviceInfo.PublicKey) < 32 {
		b.logger.Warn("Cannot renew auth token: no device info")
		return
	}

	// Require device private key for JWT signing
	if devicePrivKey == "" {
		b.logger.Warn("Cannot renew auth token: device_private_key not configured",
			"region", region, "hint", "Set DevicePrivKey in MQTTConfig for Let's Mesh Analyzer")
		return
	}

	// Determine server name based on region
	serverName := "mqtt-us-v1.letsmesh.net"
	if region == "eu" {
		serverName = "mqtt-eu-v1.letsmesh.net"
	}

	// Generate JWT token with Ed25519 signing
	expiresIn := 86400 // 24 hours
	jwtToken, err := b.createSignedJWT(deviceInfo.PublicKey, devicePrivKey, ownerEmail, serverName, expiresIn)
	if err != nil {
		b.logger.Error("Failed to create signed JWT", "region", region, "error", err)
		return
	}

	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	switch region {
	case "us":
		b.authTokenUS = jwtToken
		b.tokenExpiresAtUS = expiresAt
	case "eu":
		b.authTokenEU = jwtToken
		b.tokenExpiresAtEU = expiresAt
	}

	b.logger.Info("Auth token generated", "region", region, "expires", expiresAt,
		"username", b.analyzerUsername)
}

// createSignedJWT creates a JWT token signed with Ed25519 for Let's Mesh Analyzer auth.
//
// MeshCore devices use the orlp/ed25519 C library, which stores private keys in a
// PRE-EXPANDED format: [clamped_scalar(32) | nonce_prefix(32)] — NOT as a seed.
//
// Go's crypto/ed25519.PrivateKey expects [seed(32) | pubkey(32)] and internally
// runs SHA-512 over the seed to derive (scalar, prefix).  Passing an orlp key
// directly to Go would cause Go to SHA-512-hash the already-clamped scalar,
// yielding a completely wrong signature.
//
// Supported key formats (from MeshCore identity.export()):
//   - 32 bytes  (64 hex):  Standard Ed25519 seed — use Go stdlib
//   - 64 bytes (128 hex):  Orlp expanded key [scalar|prefix] — use orlp-compatible signing
//   - 96 bytes (192 hex):  Full MeshCore export [prv(64)|pub(32)] — extract first 64 bytes
func (b *MQTTBridge) createSignedJWT(publicKey []byte, privateKeyHex, email, serverName string, expiresIn int) (string, error) {
	privKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return "", fmt.Errorf("invalid private key hex: %w", err)
	}

	now := time.Now()
	iat := now.Unix()
	exp := now.Add(time.Duration(expiresIn) * time.Second).Unix()

	// Build JWT header + payload (identical for all key formats).
	headerJSON, _ := json.Marshal(map[string]string{"alg": "Ed25519", "typ": "JWT"})
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)

	hexPubKey := strings.ToUpper(hex.EncodeToString(publicKey))
	payloadJSON, _ := json.Marshal(map[string]interface{}{
		"aud":       serverName,
		"email":     email,
		"exp":       exp,
		"iat":       iat,
		"publicKey": hexPubKey,
	})
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)

	signingInput := []byte(headerB64 + "." + payloadB64)

	var sig []byte

	switch len(privKeyBytes) {
	case 32:
		// Standard Ed25519 seed: Go stdlib handles SHA-512 expansion correctly.
		stdKey := ed25519.NewKeyFromSeed(privKeyBytes)
		sig = ed25519.Sign(stdKey, signingInput)

	case 64:
		// Orlp expanded key: [clamped_scalar(32) | nonce_prefix(32)].
		// Must NOT pass to ed25519.PrivateKey — Go would re-expand it incorrectly.
		sig, err = b.signWithOrlpKey(signingInput, privKeyBytes, publicKey)
		if err != nil {
			return "", fmt.Errorf("orlp signing failed: %w", err)
		}

	case 96:
		// Full MeshCore export: [prv_key(64) | pub_key(32)].
		// Verify the embedded public key matches what we expect.
		if !bytes.Equal(privKeyBytes[64:], publicKey) {
			b.logger.Warn("Public key in 96-byte export does not match device public key",
				"exported", hex.EncodeToString(privKeyBytes[64:]),
				"device", hex.EncodeToString(publicKey))
		}
		sig, err = b.signWithOrlpKey(signingInput, privKeyBytes[:64], publicKey)
		if err != nil {
			return "", fmt.Errorf("orlp signing failed: %w", err)
		}

	default:
		return "", fmt.Errorf("invalid private key length: expected 32, 64, or 96 bytes, got %d", len(privKeyBytes))
	}

	// MeshCore JWT format: header.payload.<hex-encoded signature>
	jwt := string(signingInput) + "." + hex.EncodeToString(sig)

	b.logger.Info("Auth token created",
		"public_key", hexPubKey,
		"audience", serverName,
		"expires", time.Unix(exp, 0).Format(time.RFC3339))

	return jwt, nil
}

// signWithOrlpKey signs a message using Ed25519 with a pre-expanded private key in
// MeshCore's orlp/ed25519 format: [clamped_scalar(32) | nonce_prefix(32)].
//
// This replicates what orlp/ed25519's ed25519_sign() does:
//
//	r  = SHA-512(prefix ‖ message) mod l        (deterministic nonce scalar)
//	R  = r·B                                    (nonce point)
//	k  = SHA-512(R ‖ pubKey ‖ message) mod l    (challenge scalar)
//	s  = (r + k·a) mod l                        (response; a is the clamped scalar)
//	sig = R ‖ s                                 (64 bytes)
func (b *MQTTBridge) signWithOrlpKey(message, expandedPrivKey, pubKey []byte) ([]byte, error) {
	if len(expandedPrivKey) != 64 {
		return nil, fmt.Errorf("expanded private key must be 64 bytes, got %d", len(expandedPrivKey))
	}
	if len(pubKey) != 32 {
		return nil, fmt.Errorf("public key must be 32 bytes, got %d", len(pubKey))
	}

	scalarBytes := expandedPrivKey[:32] // pre-clamped scalar 'a'
	prefix := expandedPrivKey[32:64]    // nonce prefix

	h := sha512.New()

	// r = SHA-512(prefix ‖ message) mod l
	h.Write(prefix)
	h.Write(message)
	r, err := new(edwards25519.Scalar).SetUniformBytes(h.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("nonce scalar: %w", err)
	}

	// R = r·B
	R := new(edwards25519.Point).ScalarBaseMult(r)
	RBytes := R.Bytes()

	// k = SHA-512(R ‖ pubKey ‖ message) mod l
	h.Reset()
	h.Write(RBytes)
	h.Write(pubKey)
	h.Write(message)
	k, err := new(edwards25519.Scalar).SetUniformBytes(h.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("challenge scalar: %w", err)
	}

	// a = clamped scalar (SetBytesWithClamping is idempotent on already-clamped data)
	a, err := new(edwards25519.Scalar).SetBytesWithClamping(scalarBytes)
	if err != nil {
		return nil, fmt.Errorf("private key scalar: %w", err)
	}

	// s = r + k·a (mod l)
	s := new(edwards25519.Scalar).MultiplyAdd(k, a, r)

	sig := make([]byte, 64)
	copy(sig[:32], RBytes)
	copy(sig[32:], s.Bytes())
	return sig, nil
}

// CreateAuthToken generates a JWT token for Let's Mesh Analyzer (public API).
func (b *MQTTBridge) CreateAuthToken(serverName string, expiresInSeconds int) (string, error) {
	b.mu.RLock()
	deviceInfo := b.deviceInfo
	privKey := b.config.DevicePrivKey
	email := b.config.OwnerEmail
	b.mu.RUnlock()

	if deviceInfo == nil || len(deviceInfo.PublicKey) < 32 {
		return "", fmt.Errorf("no device info available")
	}
	if privKey == "" {
		return "", fmt.Errorf("DevicePrivKey not configured")
	}
	return b.createSignedJWT(deviceInfo.PublicKey, privKey, email, serverName, expiresInSeconds)
}

// ============================================================================
// Utility Methods
// ============================================================================

// isAnyConnected checks if any MQTT broker is connected
func (b *MQTTBridge) isAnyConnected() bool {
	return b.customConnected.Load() || b.analyzerUSConnected.Load() || b.analyzerEUConnected.Load()
}

// GetConnectionState returns the connection state of all brokers
func (b *MQTTBridge) GetConnectionState() []BrokerInfo {
	var brokers []BrokerInfo

	brokers = append(brokers, BrokerInfo{
		Name:      "custom",
		Connected: b.customConnected.Load(),
		URL:       fmt.Sprintf("%s:%d", b.config.Server, b.config.Port),
	})

	brokers = append(brokers, BrokerInfo{
		Name:      "us",
		Connected: b.analyzerUSConnected.Load(),
		URL:       analyzerUSURL,
	})

	brokers = append(brokers, BrokerInfo{
		Name:      "eu",
		Connected: b.analyzerEUConnected.Load(),
		URL:       analyzerEUURL,
	})

	return brokers
}

// GetQueueSize returns the current packet queue size
func (b *MQTTBridge) GetQueueSize() int {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	return len(b.queue)
}

// IsConfigValid checks if MQTT configuration is valid
func (b *MQTTBridge) IsConfigValid() bool {
	hasCustom := b.isCustomBrokerConfigured()
	hasAnalyzer := b.config.AnalyzerUS || b.config.AnalyzerEU

	if !hasCustom && !hasAnalyzer {
		return false
	}

	if b.config.PacketsEnabled && b.config.IATA == "" {
		return false
	}

	return true
}

// SetConfig updates the MQTT configuration
func (b *MQTTBridge) SetConfig(config *MQTTConfig) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.config = config
	b.statusInterval = time.Duration(config.StatusInterval) * time.Millisecond
}

// GetConfig returns the current MQTT configuration
func (b *MQTTBridge) GetConfig() *MQTTConfig {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.config
}

// ============================================================================
// WebSocket Support
// ============================================================================

// buildWebSocketURL constructs a WebSocket URL for MQTT over WebSocket
func (b *MQTTBridge) buildWebSocketURL(host string, port int, useTLS bool) string {
	scheme := "ws"
	if useTLS {
		scheme = "wss"
	}
	return fmt.Sprintf("%s://%s:%d/mqtt", scheme, host, port)
}

// createWebSocketClient creates an MQTT client using WebSocket transport
func (b *MQTTBridge) createWebSocketClient(clientID, brokerURL, username, password string,
	caCert string) (mqtt.Client, error) {

	opts := mqtt.NewClientOptions()
	opts.AddBroker(brokerURL)
	opts.SetClientID(fmt.Sprintf("meshcore-ws-%s-%d", clientID, time.Now().UnixNano()))
	opts.SetUsername(username)
	opts.SetPassword(password)
	opts.SetKeepAlive(defaultKeepAlive * time.Second)
	opts.SetConnectTimeout(defaultConnectTimeout * time.Second)
	opts.SetAutoReconnect(true)

	// Configure WebSocket transport
	opts.SetHTTPHeaders(http.Header{
		"Sec-WebSocket-Protocol": []string{"mqtt"},
	})

	// Configure TLS if needed
	if caCert != "" {
		tlsConfig := &tls.Config{}
		certPool := x509.NewCertPool()
		if certPool.AppendCertsFromPEM([]byte(caCert)) {
			tlsConfig.RootCAs = certPool
		}
		opts.SetTLSConfig(tlsConfig)
	}

	// Connection handlers
	opts.OnConnect = func(c mqtt.Client) {
		b.handleConnect(clientID)
	}
	opts.OnConnectionLost = func(c mqtt.Client, err error) {
		b.handleConnectionLost(clientID, err)
	}

	client := mqtt.NewClient(opts)
	token := client.Connect()

	if token.Wait() && token.Error() != nil {
		return nil, token.Error()
	}

	return client, nil
}

// ============================================================================
// Packet Handling
// ============================================================================

// OnPacketReceived handles incoming mesh packets for MQTT publishing
func (b *MQTTBridge) OnPacketReceived(rawData []byte, snr, rssi float64) {
	if !b.config.PacketsEnabled {
		return
	}

	if !b.IsConfigValid() {
		b.logger.Warn("MQTT config not valid, queuing packet")
	}

	b.QueuePacket(rawData, false, snr, rssi)
}

// OnPacketSent handles transmitted packets for MQTT publishing
func (b *MQTTBridge) OnPacketSent(rawData []byte) {
	if !b.config.PacketsEnabled || !b.config.TxEnabled {
		return
	}

	b.QueuePacket(rawData, true, 0, 0)
}

// ============================================================================
// Configuration Access (for compatibility with C++ API)
// ============================================================================

// FormatStatusReply returns a formatted status string
func (b *MQTTBridge) FormatStatusReply() string {
	customStatus := "disconnected"
	if b.customConnected.Load() {
		customStatus = "connected"
	}
	if !b.isCustomBrokerConfigured() {
		customStatus = "n/a"
	}

	usStatus := "off"
	if b.config.AnalyzerUS {
		if b.analyzerUSConnected.Load() {
			usStatus = "connected"
		} else {
			usStatus = "disconnected"
		}
	}

	euStatus := "off"
	if b.config.AnalyzerEU {
		if b.analyzerEUConnected.Load() {
			euStatus = "connected"
		} else {
			euStatus = "disconnected"
		}
	}

	msgs := "on"
	if !b.config.StatusEnabled {
		msgs = "off"
	}

	return fmt.Sprintf("msgs: %s, broker: %s, us: %s, eu: %s, queue: %d",
		msgs, customStatus, usStatus, euStatus, b.GetQueueSize())
}

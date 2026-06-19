package mqttbridge

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"filippo.io/edwards25519"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"meshbot/internal/proxy"
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
	defaultKeepAlive      = 45
	defaultConnectTimeout = 30
	maxQueuedPackets      = 100

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

// broker holds all per-connection state for one MQTT endpoint (custom, us, eu).
// One *broker per endpoint lives in MQTTBridge.brokers; that map is built once in
// NewMQTTBridge and never has keys added or removed afterwards, so reads of it are
// safe without a lock. Field-level locking:
//   - connected is an atomic, set in handleConnect/handleConnectionLost (and on
//     forced teardown) and read lock-free everywhere else.
//   - client and the reconnect bookkeeping (baseDelay/reconnectDelay/lastReconnect)
//     are guarded by MQTTBridge.clientMu.
//   - authToken/tokenExpires (analyzer only) are touched only by the single
//     connectionManager goroutine (connect → getAuthToken → renewAuthToken), so
//     they need no lock.
type broker struct {
	name      string
	connected atomic.Bool

	// Guarded by MQTTBridge.clientMu.
	client         mqtt.Client
	baseDelay      time.Duration // first backoff delay / floor after a successful connect
	reconnectDelay time.Duration // current backoff window; doubles up to maxReconnectDelay
	lastReconnect  time.Time

	// Analyzer JWT (unused for custom). Touched only by the connectionManager
	// goroutine, so no lock is needed.
	authToken    string
	tokenExpires time.Time
}

// maxReconnectDelay caps the exponential reconnect backoff.
const maxReconnectDelay = 300 * time.Second

// newBroker creates a broker whose reconnect backoff starts (and refloors after a
// successful connect) at baseDelay.
func newBroker(name string, baseDelay time.Duration) *broker {
	return &broker{name: name, baseDelay: baseDelay, reconnectDelay: baseDelay}
}

// MQTTBridge handles MQTT connectivity for MeshCore
type MQTTBridge struct {
	// config is set once in NewMQTTBridge and treated as immutable thereafter, so
	// it is read without a lock. statusInterval is derived from it, also once.
	config *MQTTConfig
	logger *slog.Logger

	// deviceInfo, stats and analyzerUsername are mutated by the Set* methods as
	// self-info/stats arrive from the device, and read on other goroutines; all
	// access is guarded by b.mu.
	deviceInfo *DeviceInfo
	stats      *DeviceStats

	// Per-broker connection state, keyed by "custom", "us", "eu". Populated once
	// in NewMQTTBridge; keys are never added or removed after construction.
	brokers map[string]*broker

	// Packet queue
	queue   []QueuedPacket
	queueMu sync.Mutex

	// notifyCh wakes the publisher goroutine when a packet is enqueued.
	// Buffered to depth 1: a single pending signal coalesces a burst of
	// enqueues into one drain pass.
	notifyCh chan struct{}

	// clientMu guards the three mqtt.Client pointers below and the reconnect
	// bookkeeping maps. These are touched by the connectionManager goroutine,
	// the publisher goroutine (on publish failure), and paho's own callback
	// goroutines (handleConnect/handleConnectionLost), so every access must
	// hold this lock. Never hold it across a blocking network call
	// (Connect/Publish/Disconnect) — snapshot the pointer, release, then call.
	clientMu sync.Mutex

	// Status publishing
	lastStatusPublish time.Time
	statusInterval    time.Duration

	// Shared analyzer auth identity; per-broker JWTs live on each broker.
	analyzerUsername string

	// Callbacks
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
		config:         config,
		logger:         slog.Default().With("module", "mqttbridge"),
		brokers:        make(map[string]*broker, 3),
		queue:          make([]QueuedPacket, 0, maxQueuedPackets),
		statusInterval: time.Duration(config.StatusInterval) * time.Millisecond,
		done:           make(chan struct{}),
		notifyCh:       make(chan struct{}, 1),
	}

	// Seed the three endpoints with their base reconnect delays. These keys are
	// the only ones ever present in the map. The custom TCP broker retries more
	// aggressively (15s) than the analyzer WebSocket endpoints (60s).
	bridge.brokers["custom"] = newBroker("custom", 15*time.Second)
	bridge.brokers["us"] = newBroker("us", 60*time.Second)
	bridge.brokers["eu"] = newBroker("eu", 60*time.Second)

	return bridge
}

// broker returns the broker for the given id, or nil for an unknown id.
func (b *MQTTBridge) broker(id string) *broker {
	return b.brokers[id]
}

// SetLogger sets a custom logger
func (b *MQTTBridge) SetLogger(logger *slog.Logger) {
	b.logger = logger.With("module", "mqttbridge")
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

// analyzerUser returns the analyzer auth identity ("v1_{PUBKEY}"). It is guarded
// by b.mu because SetDeviceInfo may (re)compute it as device self-info arrives.
func (b *MQTTBridge) analyzerUser() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.analyzerUsername
}

// SetDeviceStats sets radio statistics for status messages
func (b *MQTTBridge) SetDeviceStats(stats *DeviceStats) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stats = stats
}

// SetCallbacks sets event callbacks
func (b *MQTTBridge) SetCallbacks(callbacks MQTTCallbacks) {
	b.onConnected = callbacks.OnConnected
	b.onDisconnected = callbacks.OnDisconnected
}

// Start begins the MQTT connection process
func (b *MQTTBridge) Start() error {
	b.mu.Lock()
	if b.done == nil {
		b.done = make(chan struct{})
	}
	if b.notifyCh == nil {
		b.notifyCh = make(chan struct{}, 1)
	}
	b.mu.Unlock()

	b.wg.Add(2)
	go b.connectionManager()
	go b.publisher()

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
		if b.config.OwnerEmail != "" {
			if b.config.AnalyzerUS {
				b.connectToAnalyzer("us", analyzerUSURL)
			}
			if b.config.AnalyzerEU {
				b.connectToAnalyzer("eu", analyzerEUURL)
			}
		} else {
			// Log once per connection cycle to avoid spam
			if !b.brokers["us"].connected.Load() && !b.brokers["eu"].connected.Load() {
				b.logger.Debug("Skipping Let's Mesh Analyzer: OwnerEmail not configured")
			}
		}

		// Packet publishing is owned by the publisher goroutine; the
		// connection manager only handles connect/reconnect and status.
		// Nudge the publisher in case packets are queued and a broker has
		// just (re)connected.
		b.signalQueue()

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
	br := b.brokers["custom"]
	if br.connected.Load() {
		return
	}
	if !b.beginReconnect(br) {
		return
	}

	b.logger.Info("Connecting to custom MQTT broker",
		"server", b.config.Server,
		"port", b.config.Port)

	brokerURL := b.buildBrokerURL(b.config.Server, b.config.Port, b.config.UseTLS)

	// autoReconnect=false: connectionManager owns all reconnection for the TCP
	// broker. Paho auto-reconnect must be off to prevent it from internally
	// queuing QoS-1 messages and "completing" their tokens without actual PUBACK
	// from the broker, which caused silent publish failures on every 2nd+ update.
	// createMQTTClient blocks on Connect(), so it must run without clientMu held.
	client, err := b.createMQTTClient("custom", "custom", brokerURL,
		b.config.Username, b.config.Password,
		b.config.UseTLS, "", "", "", false, false)

	if err != nil {
		b.logger.Error("Failed to create custom MQTT client", "error", err)
		b.clientMu.Lock()
		b.scheduleReconnect(br)
		b.clientMu.Unlock()
		return
	}

	b.clientMu.Lock()
	br.client = client
	// Record the connection attempt time so that connectionManager does not
	// immediately spin up another client on the next 5-second tick if the
	// OnConnect callback hasn't fired yet.
	br.lastReconnect = time.Now()
	b.clientMu.Unlock()
}

// connectToAnalyzer establishes connection to Let's Mesh Analyzer server
func (b *MQTTBridge) connectToAnalyzer(region, brokerURL string) {
	br := b.brokers[region]
	if br == nil || (region != "us" && region != "eu") {
		return
	}
	if br.connected.Load() {
		return
	}
	if !b.beginReconnect(br) {
		return
	}

	b.logger.Info("Connecting to Let's Mesh Analyzer", "region", region, "url", brokerURL)

	// For analyzer servers, use JWT token as password
	password := b.getAuthToken(region)
	if password == "" {
		b.logger.Warn("No auth token available for analyzer", "region", region)
		b.clientMu.Lock()
		b.scheduleReconnect(br)
		b.clientMu.Unlock()
		return
	}

	// Client ID must be unique per device; use region + first 8 chars of public key.
	// Using a fixed ID like "mc-us" would cause the broker to kick the previous
	// connection every time any other device using this software connects.
	username := b.analyzerUser()
	clientID := region
	if username != "" {
		// analyzerUsername is "v1_{PUBKEY}" — take 8 chars of pubkey for uniqueness
		parts := strings.SplitN(username, "_", 2)
		if len(parts) == 2 && len(parts[1]) >= 8 {
			clientID = fmt.Sprintf("%s-%s", region, strings.ToLower(parts[1][:8]))
		}
	}

	var newClient mqtt.Client
	newClient, err := b.createMQTTClient(region, clientID, brokerURL,
		username, password,
		true, gtsRootR4Cert, "", "", true, true) // Always use TLS for analyzer; paho reconnect OK for WS

	if err != nil {
		b.logger.Error("Failed to create analyzer MQTT client", "error", err, "region", region)
		b.clientMu.Lock()
		b.scheduleReconnect(br)
		b.clientMu.Unlock()
		return
	}

	b.clientMu.Lock()
	// Record the connection attempt time so connectionManager does not immediately
	// spin up another client on the next 5-second tick before OnConnect fires.
	br.lastReconnect = time.Now()
	br.client = newClient
	b.clientMu.Unlock()
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
// brokerKey is the logical name used in callbacks ("custom", "us", "eu");
// clientID is the unique MQTT protocol identifier sent to the broker.
func (b *MQTTBridge) createMQTTClient(brokerKey, clientID, brokerURL, username, password string,
	useTLS bool, caCert, clientCert, clientKey string, autoReconnect bool, useProxy bool) (mqtt.Client, error) {

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

	// Apply SOCKS5 proxy if configured and requested for this connection.
	if useProxy && proxy.Dialer() != nil {
		d := proxy.Dialer()
		u, _ := url.Parse(brokerURL)
		switch u.Scheme {
		case "ws", "wss":
			proxyURL := proxy.URL()
			opts.SetWebsocketOptions(&mqtt.WebsocketOptions{
				Proxy: func(_ *http.Request) (*url.URL, error) {
					return url.Parse(proxyURL)
				},
			})
		default: // tcp, ssl, mqtt, mqtts
			opts.SetCustomOpenConnectionFn(func(uri *url.URL, _ mqtt.ClientOptions) (net.Conn, error) {
				return d.Dial("tcp", uri.Host)
			})
		}
	}

	// Connection handlers
	opts.OnConnect = func(c mqtt.Client) {
		b.handleConnect(brokerKey)
	}
	opts.OnConnectionLost = func(c mqtt.Client, err error) {
		b.handleConnectionLost(brokerKey, err)
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

	br := b.brokers[brokerID]
	if br == nil {
		return
	}

	br.connected.Store(true)
	b.clientMu.Lock()
	// Successful connect: refloor the backoff to this endpoint's base delay.
	br.reconnectDelay = br.baseDelay
	b.clientMu.Unlock()

	if b.onConnected != nil {
		b.onConnected(brokerID)
	}
}

// handleConnectionLost handles connection loss
func (b *MQTTBridge) handleConnectionLost(brokerID string, err error) {
	b.logger.Warn("MQTT connection lost", "broker", brokerID, "error", err)

	br := b.brokers[brokerID]
	if br == nil {
		return
	}

	br.connected.Store(false)

	if b.onDisconnected != nil {
		b.onDisconnected(brokerID)
	}
}

// beginReconnect gates a connection attempt by the backoff window and detaches
// any existing client so a fresh one can be created without two clients sharing a
// client ID (which would make the broker kick them in an infinite loop). It
// returns false if the backoff window has not yet elapsed, in which case the
// caller should skip this attempt. The old client is disconnected outside the
// lock because Disconnect blocks.
func (b *MQTTBridge) beginReconnect(br *broker) bool {
	b.clientMu.Lock()
	if !b.canReconnect(br) {
		b.clientMu.Unlock()
		return false
	}
	old := br.client
	br.client = nil
	b.clientMu.Unlock()

	if old != nil {
		old.Disconnect(250)
	}
	return true
}

// canReconnect checks if enough time has passed to attempt reconnection.
// Caller must hold b.clientMu (it reads the reconnect bookkeeping fields).
func (b *MQTTBridge) canReconnect(br *broker) bool {
	if !br.lastReconnect.IsZero() && time.Since(br.lastReconnect) < br.reconnectDelay {
		return false
	}
	return true
}

// scheduleReconnect arms the next reconnect attempt with exponential backoff:
// the current delay doubles on each failure, floored at the broker's base delay
// and capped at maxReconnectDelay. handleConnect refloors it on success.
// Caller must hold b.clientMu (it mutates the reconnect bookkeeping fields).
func (b *MQTTBridge) scheduleReconnect(br *broker) {
	br.lastReconnect = time.Now()

	delay := br.reconnectDelay * 2
	if delay < br.baseDelay {
		delay = br.baseDelay
	}
	if delay > maxReconnectDelay {
		delay = maxReconnectDelay
	}

	br.reconnectDelay = delay
	b.logger.Info("Scheduled reconnect", "broker", br.name, "delay", delay)
}

// resetCustomConnection tears down the custom broker client and clears all
// related state so that connectionManager will immediately attempt to reconnect
// on the next 5-second tick. Call this whenever a publish fails or times out.
//
// failed is the client the caller was publishing on. If the connection manager
// has already swapped in a newer client, the reset is skipped so a stale publish
// error cannot tear down a freshly established connection. Pass nil to force a
// reset of whatever client is current.
func (b *MQTTBridge) resetCustomConnection(failed mqtt.Client) {
	br := b.brokers["custom"]
	br.connected.Store(false)

	b.clientMu.Lock()
	if failed != nil && br.client != failed {
		// A newer client already replaced the failed one; leave it alone.
		b.clientMu.Unlock()
		return
	}
	// Zero out lastReconnect so canReconnect() allows an immediate retry.
	br.lastReconnect = time.Time{}
	old := br.client
	br.client = nil
	b.clientMu.Unlock()

	if old != nil {
		// Best-effort graceful disconnect; don't block the caller.
		go old.Disconnect(250)
	}
	b.logger.Warn("Custom broker connection reset; will reconnect on next tick")
}

// disconnectAll disconnects all MQTT clients
func (b *MQTTBridge) disconnectAll() {
	// Snapshot the client pointers under the lock, then disconnect outside it
	// (Disconnect blocks for up to its quiesce timeout).
	b.clientMu.Lock()
	clients := make([]mqtt.Client, 0, len(b.brokers))
	for _, br := range b.brokers {
		clients = append(clients, br.client)
	}
	b.clientMu.Unlock()

	for _, c := range clients {
		if c != nil && c.IsConnected() {
			c.Disconnect(1000)
		}
	}

	for _, br := range b.brokers {
		br.connected.Store(false)
	}
}

// ============================================================================
// Packet Publishing
// ============================================================================

// QueuePacket adds a packet to the queue for publishing. Transmitted packets are
// only published when TxEnabled is set; received packets are always queued.
func (b *MQTTBridge) QueuePacket(rawData []byte, isTX bool, snr, rssi float64) {
	if isTX && !b.config.TxEnabled {
		return
	}
	b.enqueue(QueuedPacket{
		RawData:   rawData,
		Timestamp: time.Now(),
		IsTX:      isTX,
		SNR:       snr,
		RSSI:      rssi,
		Hash:      calculatePacketHash(rawData),
	})
}

// QueueChannelPacket queues a raw RF packet that has been correlated with a
// decoded BLE channel message frame. The decoded fields (channel name, sender
// name, message text) are embedded in the published JSON so the web UI can
// display them without needing to parse or decrypt the raw bytes.
func (b *MQTTBridge) QueueChannelPacket(rawData []byte, isTX bool, snr, rssi float64, channelIndex int, channelName, senderName, text, channelKeyHex string) {
	if isTX && !b.config.TxEnabled {
		return
	}
	b.enqueue(QueuedPacket{
		RawData:          rawData,
		Timestamp:        time.Now(),
		IsTX:             isTX,
		SNR:              snr,
		RSSI:             rssi,
		Hash:             calculatePacketHash(rawData),
		IsChannelMessage: true,
		ChannelIndex:     channelIndex,
		ChannelName:      channelName,
		SenderName:       senderName,
		Text:             text,
		ChannelKey:       channelKeyHex,
	})
}

// enqueue appends a fully-built packet to the publish queue, evicting the oldest
// entry when the queue is full, then wakes the publisher goroutine.
func (b *MQTTBridge) enqueue(packet QueuedPacket) {
	b.queueMu.Lock()
	if len(b.queue) >= maxQueuedPackets {
		b.queue = b.queue[1:]
		b.logger.Warn("Packet queue full, dropping oldest")
	}
	b.queue = append(b.queue, packet)
	queueLen := len(b.queue)
	b.queueMu.Unlock()

	if packet.IsChannelMessage {
		b.logger.Debug("Channel packet queued", "channel", packet.ChannelIndex,
			"sender", packet.SenderName, "isTX", packet.IsTX, "queue_len", queueLen)
	} else {
		b.logger.Debug("Packet queued", "hash", packet.Hash, "queue_len", queueLen)
	}
	b.signalQueue()
}

// calculatePacketHash mirrors MeshCore's Packet::calculatePacketHash:
// SHA-256 over [payload_type(1) | path_len(1, TRACE only) | payload], truncated to 8 bytes.
// PAYLOAD_TYPE_TRACE = 9 (from Dispatcher.cpp / MQTT spec).
func calculatePacketHash(data []byte) string {
	payloadType := uint8(getPayloadType(data))
	payload := extractPayload(data)

	h := sha256.New()
	h.Write([]byte{payloadType})
	if payloadType == 9 { // PAYLOAD_TYPE_TRACE
		h.Write([]byte{getPathLenByte(data)})
	}
	h.Write(payload)

	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8]) // MAX_HASH_SIZE = 8
}

// pathLenOffset returns the index of the path_len byte in a raw packet, or -1 if
// the packet is too short to contain a header. TRANSPORT routes (route type 0x00
// or 0x03) carry an extra 4-byte transport_codes field before path_len.
func pathLenOffset(data []byte) int {
	if len(data) < 2 {
		return -1
	}
	headerLen := 2
	if routeType := data[0] & 0x03; routeType == 0x00 || routeType == 0x03 {
		headerLen = 6
	}
	if len(data) < headerLen {
		return -1
	}
	return headerLen - 1
}

// payloadOffset returns the index at which the payload begins (after the header
// and the variable-length path), or -1 if the packet is malformed or carries no
// payload. The path_len byte encodes hash_count (bits 0-5) and bytes-per-hop
// (bits 6-7, +1 → 1/2/3); the path occupies hash_count * bytes-per-hop bytes.
func payloadOffset(data []byte) int {
	i := pathLenOffset(data)
	if i < 0 {
		return -1
	}
	pathLenByte := data[i]
	hashCount := int(pathLenByte & 0x3F)
	hashSize := int((pathLenByte>>6)&0x03) + 1
	offset := i + 1 + hashCount*hashSize
	if offset >= len(data) {
		return -1
	}
	return offset
}

// extractPayload returns the payload slice from a raw packet.
func extractPayload(data []byte) []byte {
	off := payloadOffset(data)
	if off < 0 {
		return nil
	}
	return data[off:]
}

// getPathLenByte returns the path_len byte from a raw packet.
func getPathLenByte(data []byte) uint8 {
	i := pathLenOffset(data)
	if i < 0 {
		return 0
	}
	return data[i]
}

// signalQueue wakes the publisher goroutine. It never blocks: notifyCh is
// buffered to depth 1, so if a wake-up is already pending this is a no-op and
// the enqueuing caller (the BLE receive path) is never stalled on MQTT.
func (b *MQTTBridge) signalQueue() {
	select {
	case b.notifyCh <- struct{}{}:
	default:
	}
}

// publisher owns packet publishing. It runs independently of the
// connectionManager's 5-second connect/reconnect cadence so a slow connect or
// reconnect backoff never delays draining the queue. It wakes on enqueue
// (notifyCh) and on a short ticker that retries packets left queued while no
// broker was connected.
func (b *MQTTBridge) publisher() {
	defer b.wg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-b.done:
			return
		case <-b.notifyCh:
		case <-ticker.C:
		}
		b.drainQueue()
	}
}

// drainQueue publishes queued packets until the queue is empty or no broker is
// connected. Each packet is fanned out to all brokers in parallel inside
// publishPacket, so one slow broker does not hold up the others.
func (b *MQTTBridge) drainQueue() {
	for {
		select {
		case <-b.done:
			return
		default:
		}

		// Leave packets queued while disconnected; the ticker retries them
		// once a broker comes back.
		if !b.isAnyConnected() {
			return
		}

		b.queueMu.Lock()
		if len(b.queue) == 0 {
			b.queueMu.Unlock()
			return
		}
		packet := b.queue[0]
		b.queue = b.queue[1:]
		b.queueMu.Unlock()

		b.publishPacket(&packet)
	}
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
	origin := deviceInfo.Name
	if origin == "" {
		origin = "Unknown"
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
		Hash:       packet.Hash,
	}

	if !packet.IsTX {
		mqttPacket.SNR = fmt.Sprintf("%.1f", packet.SNR)
		mqttPacket.RSSI = fmt.Sprintf("%.1f", packet.RSSI)
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

	// Fan out to every connected broker in parallel so a slow or dead broker
	// cannot stall publishing to the others.
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		b.publishCustomOne(topic, jsonData, false)
	}()
	go func() {
		defer wg.Done()
		b.publishAnalyzers(topic, jsonData, false)
	}()
	wg.Wait()

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

	b.publishAnalyzers(topic, jsonData, false)
}

// publishAnalyzers publishes a payload to both analyzer servers concurrently.
// retained controls the MQTT retained flag (true for status, false for packets).
// It uses paho's IsConnected() as the source of truth at publish time so a broker
// whose atomic flag lags behind the live connection is not skipped.
func (b *MQTTBridge) publishAnalyzers(topic string, data []byte, retained bool) {
	b.clientMu.Lock()
	us := b.brokers["us"].client
	eu := b.brokers["eu"].client
	b.clientMu.Unlock()

	var wg sync.WaitGroup

	for _, t := range []struct {
		client mqtt.Client
		label  string
	}{
		{us, "US"},
		{eu, "EU"},
	} {
		if t.client == nil || !t.client.IsConnected() {
			continue
		}
		wg.Add(1)
		go func(client mqtt.Client, label string) {
			defer wg.Done()
			b.publishAnalyzerOne(client, label, topic, data, retained)
		}(t.client, t.label)
	}

	wg.Wait()
}

// publishCustomOne publishes a single payload to the custom TCP broker, waiting
// up to customPublishTimeout for the PUBACK. A timeout or error resets the
// connection (passing the publishing client so a stale failure cannot tear down
// a freshly reconnected one). Returns false if the broker was not connected.
func (b *MQTTBridge) publishCustomOne(topic string, data []byte, retained bool) bool {
	const customPublishTimeout = 15 * time.Second

	br := b.brokers["custom"]
	b.clientMu.Lock()
	custom := br.client
	b.clientMu.Unlock()

	if custom == nil || !custom.IsConnected() {
		return false
	}

	token := custom.Publish(topic, 1, retained, data)
	if !token.WaitTimeout(customPublishTimeout) {
		b.logger.Warn("Publish to custom broker timed out, resetting connection", "topic", topic)
		b.resetCustomConnection(custom)
	} else if token.Error() != nil {
		b.logger.Error("Failed to publish to custom broker", "error", token.Error(), "topic", topic)
		b.resetCustomConnection(custom)
	}
	return true
}

// publishAnalyzerOne publishes a single payload to one analyzer broker and
// logs the outcome. WaitTimeout prevents blocking forever when the connection
// drops mid-publish (paho autoReconnect would otherwise keep the token pending
// across reconnects).
func (b *MQTTBridge) publishAnalyzerOne(client mqtt.Client, label, topic string, data []byte, retained bool) {
	const analyzerPublishTimeout = 15 * time.Second
	token := client.Publish(topic, 1, retained, data)
	if !token.WaitTimeout(analyzerPublishTimeout) {
		b.logger.Warn("Publish to analyzer timed out", "broker", label, "topic", topic)
	} else if token.Error() != nil {
		b.logger.Error("Failed to publish to analyzer", "broker", label, "error", token.Error(), "topic", topic)
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
func getPayloadLen(data []byte) int {
	off := payloadOffset(data)
	if off < 0 {
		return 0
	}
	return len(data) - off
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
			"custom", b.brokers["custom"].connected.Load(),
			"analyzer_us", b.brokers["us"].connected.Load(),
			"analyzer_eu", b.brokers["eu"].connected.Load())
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
		"custom_connected", b.brokers["custom"].connected.Load())
	b.publishStatus()
}

// publishStatus publishes a status message to all connected brokers.
// The message is marked as retained so newly connected subscribers receive
// the latest state immediately.
func (b *MQTTBridge) publishStatus() {
	b.mu.RLock()
	deviceInfo := b.deviceInfo
	stats := b.stats
	b.mu.RUnlock()

	if deviceInfo == nil {
		b.logger.Warn("Cannot publish status: no device info")
		return
	}

	originID := hex.EncodeToString(deviceInfo.PublicKey)
	origin := deviceInfo.Name
	if origin == "" {
		origin = "Unknown"
	}

	// Build status message with all available device parameters.
	model := deviceInfo.Model
	if model == "" {
		model = "MeshCore"
	}
	fwVersion := deviceInfo.FirmwareVersion
	if fwVersion == "" {
		fwVersion = "unknown"
	}
	mqttStatus := &MQTTStatus{
		Status:          "online",
		Timestamp:       time.Now().UTC().Format("2006-01-02T15:04:05.000000"),
		Origin:          origin,
		OriginID:        originID,
		Model:           model,
		FirmwareVersion: fwVersion,
		Radio:           buildRadioString(deviceInfo),
		ClientVersion:   "meshcore/" + fwVersion,
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

	// Publish to custom broker and to the analyzer servers, all retained so newly
	// connected subscribers immediately receive the latest state.
	b.publishCustomOne(topic, jsonData, true)
	b.publishAnalyzers(topic, jsonData, true)

	b.lastStatusPublish = time.Now()
	b.logger.Info("Status published", "topic", topic,
		"lat", mqttStatus.Lat, "lon", mqttStatus.Lon,
		"tx_power", mqttStatus.TxPower,
		"has_stats", mqttStatus.Stats != nil)
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

// ============================================================================
// Authentication
// ============================================================================

// getAuthToken returns the current auth token for analyzer servers
func (b *MQTTBridge) getAuthToken(region string) string {
	br := b.brokers[region]
	if br == nil {
		return ""
	}

	// Renew token if expiring soon
	if time.Until(br.tokenExpires) < 5*time.Minute {
		b.renewAuthToken(region)
	}

	return br.authToken
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

	if br := b.brokers[region]; br != nil {
		br.authToken = jwtToken
		br.tokenExpires = expiresAt
	}

	b.logger.Info("Auth token generated", "region", region, "expires", expiresAt,
		"username", b.analyzerUser())
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
	claims := map[string]interface{}{
		"aud":       serverName,
		"email":     email,
		"exp":       exp,
		"iat":       iat,
		"publicKey": hexPubKey,
	}
	if ownerPubKey := b.config.OwnerPublicKey; ownerPubKey != "" {
		claims["owner"] = strings.ToUpper(ownerPubKey)
	}
	payloadJSON, _ := json.Marshal(claims)
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

// ============================================================================
// Utility Methods
// ============================================================================

// isAnyConnected checks if any MQTT broker is connected
func (b *MQTTBridge) isAnyConnected() bool {
	return b.brokers["custom"].connected.Load() ||
		b.brokers["us"].connected.Load() ||
		b.brokers["eu"].connected.Load()
}

// DevicePrivKey returns the configured device private key (hex), or "" if unset.
// It lets the bridge satisfy the meshcore_bt.PacketPublisher interface without
// exposing the whole config.
func (b *MQTTBridge) DevicePrivKey() string {
	if b.config == nil {
		return ""
	}
	return b.config.DevicePrivKey
}

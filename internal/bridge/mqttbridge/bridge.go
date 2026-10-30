package mqttbridge

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

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
	defaultKeepAlive      = 45
	defaultConnectTimeout = 30
	maxQueuedPackets      = 100

	// Let's Mesh Analyzer servers
	analyzerUSURL = "wss://mqtt-us-v1.letsmesh.net:443/mqtt"
	analyzerEUURL = "wss://mqtt-eu-v1.letsmesh.net:443/mqtt"

	// Topic structure
	topicPattern = "meshcore/%s/%s/%s" // meshcore/{IATA}/{DEVICE_ID}/{type}
)

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

		// Connect to every configured endpoint in parallel: each connect blocks
		// up to defaultConnectTimeout (30s), so running them sequentially let
		// one dead endpoint stall the others and delay status publishing by up
		// to ~90s per cycle. The per-broker state (backoff, JWT) each goroutine
		// touches is lock-guarded or broker-confined; the cycle joins before
		// status publishing, so exactly one connect goroutine per broker is in
		// flight at any time.
		var connectWG sync.WaitGroup
		if b.isCustomBrokerConfigured() {
			connectWG.Add(1)
			go func() {
				defer connectWG.Done()
				b.connectToCustomBroker()
			}()
		}

		// Connect to Let's Mesh Analyzer servers
		if b.config.OwnerEmail != "" {
			if b.config.AnalyzerUS {
				connectWG.Add(1)
				go func() {
					defer connectWG.Done()
					b.connectToAnalyzer("us", analyzerUSURL)
				}()
			}
			if b.config.AnalyzerEU {
				connectWG.Add(1)
				go func() {
					defer connectWG.Done()
					b.connectToAnalyzer("eu", analyzerEUURL)
				}()
			}
		} else {
			// Log once per connection cycle to avoid spam
			if !b.brokers["us"].connected.Load() && !b.brokers["eu"].connected.Load() {
				b.logger.Debug("Skipping Let's Mesh Analyzer: OwnerEmail not configured")
			}
		}
		connectWG.Wait()

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
// ============================================================================
// Packet Publishing
// ============================================================================

// QueuePacket adds a packet to the queue for publishing. The gate lives here
// (not at publish time) because the driver calls the publisher unconditionally.
func (b *MQTTBridge) QueuePacket(rawData []byte, isTX bool, snr, rssi float64) {
	if !b.config.PacketsEnabled {
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
// decoded channel message frame. The decoded fields (channel name, sender
// name, message text) are embedded in the published JSON so the web UI can
// display them without needing to parse or decrypt the raw bytes.
func (b *MQTTBridge) QueueChannelPacket(rawData []byte, isTX bool, snr, rssi float64, channelIndex int, channelName, senderName, text, channelKeyHex string) {
	if !b.config.PacketsEnabled {
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

// signalQueue wakes the publisher goroutine. It never blocks: notifyCh is
// buffered to depth 1, so if a wake-up is already pending this is a no-op and
// the enqueuing caller (the receive path) is never stalled on MQTT.
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
		// The PSK decrypts the whole channel — publish it only when the
		// operator opted in (publish_channel_keys, default off).
		if b.config.PublishChannelKeys {
			mqttPacket.ChannelKey = packet.ChannelKey
		}
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
// It lets the bridge satisfy the meshcore.PacketPublisher interface without
// exposing the whole config.
func (b *MQTTBridge) DevicePrivKey() string {
	if b.config == nil {
		return ""
	}
	return b.config.DevicePrivKey
}

// TxEnabled reports whether outgoing (transmitted) messages should be mirrored to
// MQTT. It lets the driver gate its synthetic outgoing-packet paths without
// importing the config package.
func (b *MQTTBridge) TxEnabled() bool {
	return b.config != nil && b.config.TxEnabled
}

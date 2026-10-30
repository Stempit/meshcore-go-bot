// Per-broker connection state and lifecycle: connect, paho callbacks,
// exponential backoff, and teardown for the three endpoints (custom TCP,
// analyzer US/EU).
package mqttbridge

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stempit/meshcore-go-bot/internal/proxy"
)

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
//   - authToken/tokenExpires (analyzer only) are confined to the per-broker
//     connect path: each connection cycle runs one goroutine per broker, and
//     the cycle joins before the next one starts, so only that goroutine
//     ever touches them and no lock is needed.
type broker struct {
	name      string
	connected atomic.Bool

	// Guarded by MQTTBridge.clientMu.
	client         mqtt.Client
	baseDelay      time.Duration // first backoff delay / floor after a successful connect
	reconnectDelay time.Duration // current backoff window; doubles up to maxReconnectDelay
	lastReconnect  time.Time

	// Analyzer JWT (unused for custom). Confined to the per-broker connect
	// goroutine (one per cycle, cycles never overlap), so no lock is needed.
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
		b.handleConnect(brokerKey, c)
	}
	opts.OnConnectionLost = func(c mqtt.Client, err error) {
		b.handleConnectionLost(brokerKey, c, err)
	}

	client := mqtt.NewClient(opts)
	token := client.Connect()

	if token.Wait() && token.Error() != nil {
		return nil, token.Error()
	}

	return client, nil
}

// handleConnect handles successful MQTT connection. c is the client the event
// belongs to: paho may re-establish a stale client concurrently with the
// connectionManager creating its replacement, and a stale event must not touch
// the current client's state.
func (b *MQTTBridge) handleConnect(brokerID string, c mqtt.Client) {
	b.logger.Debug("MQTT connected in handle", "broker", brokerID)

	br := b.brokers[brokerID]
	if br == nil {
		return
	}

	b.clientMu.Lock()
	if c != br.client {
		// Stale event from a replaced client — ignore it entirely.
		b.clientMu.Unlock()
		return
	}
	// Successful connect: refloor the backoff to this endpoint's base delay.
	br.connected.Store(true)
	br.reconnectDelay = br.baseDelay
	b.clientMu.Unlock()

	if b.onConnected != nil {
		b.onConnected(brokerID)
	}
}

// handleConnectionLost handles connection loss. See handleConnect for the
// stale-client guard rationale.
func (b *MQTTBridge) handleConnectionLost(brokerID string, c mqtt.Client, err error) {
	b.logger.Warn("MQTT connection lost", "broker", brokerID, "error", err)

	br := b.brokers[brokerID]
	if br == nil {
		return
	}

	b.clientMu.Lock()
	stale := c != br.client
	if !stale {
		br.connected.Store(false)
	}
	b.clientMu.Unlock()
	if stale {
		return
	}

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

package meshcore_bt

import (
	"time"
)

// ============================================================================
// MQTT Configuration Models
// ============================================================================

// MQTTConfig holds all MQTT configuration settings
type MQTTConfig struct {
	// Server settings
	Server   string `yaml:"server"`   // Broker hostname
	Port     int    `yaml:"port"`     // Broker port
	Username string `yaml:"username"` // MQTT username
	Password string `yaml:"password"` // MQTT password

	// Device identity
	Origin string `yaml:"origin"` // Device origin name (e.g., "MeshCore-Repeater-01")
	IATA   string `yaml:"iata"`   // IATA airport code (e.g., "SEA", "LAX")

	// Message settings
	StatusEnabled  bool `yaml:"status_enabled"`  // Enable status messages
	PacketsEnabled bool `yaml:"packets_enabled"` // Enable packet messages
	RawEnabled     bool `yaml:"raw_enabled"`     // Enable raw messages
	TxEnabled      bool `yaml:"tx_enabled"`      // Enable TX packet messages

	// Status interval (milliseconds)
	StatusInterval int `yaml:"status_interval"` // Status publish interval

	// Let's Mesh Analyzer servers
	AnalyzerUS bool `yaml:"analyzer_us"` // Enable US analyzer
	AnalyzerEU bool `yaml:"analyzer_eu"` // Enable EU analyzer

	// Owner info for JWT tokens
	OwnerPublicKey string `yaml:"owner_public_key"`   // 64 hex characters
	OwnerEmail     string `yaml:"owner_email"`        // Owner email
	DevicePrivKey  string `yaml:"device_private_key"` // 64 hex characters - Ed25519 private key for JWT signing

	// TLS settings
	UseTLS     bool   `yaml:"use_tls"`     // Use TLS connection
	CACert     string `yaml:"ca_cert"`     // CA Certificate
	ClientCert string `yaml:"client_cert"` // Client certificate
	ClientKey  string `yaml:"client_key"`  // Client key
}

// DefaultMQTTConfig returns a default MQTT configuration
func DefaultMQTTConfig() *MQTTConfig {
	return &MQTTConfig{
		StatusEnabled:  true,
		PacketsEnabled: true,
		RawEnabled:     false,
		TxEnabled:      false,
		StatusInterval: 300000, // 5 minutes
		AnalyzerUS:     true,
		AnalyzerEU:     true,
		Port:           1883,
	}
}

// MQTTStatus represents device status for MQTT publishing.
// Published to meshcore/{IATA}/{DEVICE_ID}/status (retained).
type MQTTStatus struct {
	Status          string     `json:"status"`             // "online" or "offline"
	Timestamp       string     `json:"timestamp"`          // ISO 8601 with microseconds (UTC)
	Origin          string     `json:"origin"`             // Device name
	OriginID        string     `json:"origin_id"`          // Device public key (64 hex chars)
	Model           string     `json:"model"`              // Hardware model string
	FirmwareVersion string     `json:"firmware_version"`   // Firmware version string
	Radio           string     `json:"radio"`              // "{freq_khz},{bw_khz},{sf},{cr}"
	ClientVersion   string     `json:"client_version"`     // "meshcore/v{version}"
	TxPower         int        `json:"tx_power,omitempty"` // TX power in dBm (omitted when 0)
	Lat             float64    `json:"lat,omitempty"`      // GPS latitude  (omitted when 0 / no fix)
	Lon             float64    `json:"lon,omitempty"`      // GPS longitude (omitted when 0 / no fix)
	Stats           *MQTTStats `json:"stats,omitempty"`    // Radio & packet stats (omitted when nil)
}

// MQTTStats holds comprehensive device statistics for status messages.
// All fields use omitempty so that genuinely-unavailable metrics are not
// emitted rather than being reported as misleading zeroes.
type MQTTStats struct {
	// Core stats
	BatteryMV  int `json:"battery_mv,omitempty"`  // Battery voltage (mV); 0 = not available
	UptimeSecs int `json:"uptime_secs,omitempty"` // Device uptime (seconds)
	Errors     int `json:"errors,omitempty"`      // Error-flags bitmask; 0 = no errors / omitted
	QueueLen   int `json:"queue_len,omitempty"`   // Outbound packet queue depth

	// Radio channel stats
	NoiseFloor int     `json:"noise_floor,omitempty"` // Channel noise floor (dBm)
	TxAirSecs  int     `json:"tx_air_secs,omitempty"` // Cumulative TX airtime (seconds)
	RxAirSecs  int     `json:"rx_air_secs,omitempty"` // Cumulative RX airtime (seconds)
	LastRssi   int     `json:"last_rssi,omitempty"`   // Last RX RSSI (dBm)
	LastSnr    float64 `json:"last_snr,omitempty"`    // Last RX SNR (dB)

	// Airtime utilisation — (TxAirSecs + RxAirSecs) / UptimeSecs * 100, rounded to 2 dp.
	// Omitted when uptime is unknown (zero).
	AirtimeUtilPct float64 `json:"airtime_util_pct,omitempty"` // Airtime utilisation (%)

	// Packet counters (STATS_TYPE_PACKETS)
	PacketsRecv     int `json:"packets_recv,omitempty"`      // Total packets received
	PacketsSent     int `json:"packets_sent,omitempty"`      // Total packets sent
	PacketsFloodTx  int `json:"packets_flood_tx,omitempty"`  // Flood-routed TX packets
	PacketsDirectTx int `json:"packets_direct_tx,omitempty"` // Direct-routed TX packets
	PacketsFloodRx  int `json:"packets_flood_rx,omitempty"`  // Flood-routed RX packets
	PacketsDirectRx int `json:"packets_direct_rx,omitempty"` // Direct-routed RX packets
	RecvErrors      int `json:"recv_errors,omitempty"`       // RadioLib CRC / receive errors
}

// MQTTPacket represents a mesh packet for MQTT publishing
type MQTTPacket struct {
	Origin     string `json:"origin"`         // Device name
	OriginID   string `json:"origin_id"`      // Device public key (64 hex)
	Timestamp  string `json:"timestamp"`      // ISO 8601 with microseconds
	Type       string `json:"type"`           // "PACKET"
	Direction  string `json:"direction"`      // "rx" or "tx"
	Time       string `json:"time"`           // UTC time HH:MM:SS
	Date       string `json:"date"`           // UTC date DD/MM/YYYY
	Len        string `json:"len"`            // Total packet length
	PacketType string `json:"packet_type"`    // Packet type code (0-15)
	Route      string `json:"route"`          // F (FLOOD), D (DIRECT), T (TRANSPORT), U (UNKNOWN)
	PayloadLen string `json:"payload_len"`    // Payload length
	Raw        string `json:"raw"`            // Raw packet data (hex)
	SNR        string `json:"SNR"`            // Signal-to-noise ratio (dB)
	RSSI       string `json:"RSSI"`           // Received signal strength (dBm)
	Hash       string `json:"hash"`           // Packet hash (32 hex)
	Path       string `json:"path,omitempty"` // Routing path for direct packets
}

// MQTTRaw represents minimal raw packet data for map integration
type MQTTRaw struct {
	Origin    string `json:"origin"`    // Device name
	OriginID  string `json:"origin_id"` // Device public key (64 hex)
	Timestamp string `json:"timestamp"` // ISO 8601 with microseconds
	Type      string `json:"type"`      // "RAW"
	Data      string `json:"data"`      // Raw packet data (hex)
}

// QueuedPacket represents a packet queued for MQTT publishing
type QueuedPacket struct {
	RawData   []byte    // Raw packet data (hex encoded)
	Timestamp time.Time // Queue timestamp
	IsTX      bool      // True if transmitted, false if received
	SNR       float64   // Signal-to-noise ratio
	RSSI      float64   // Received signal strength
	Hash      string    // Packet hash for deduplication
}

// ConnectionState represents MQTT connection state
type ConnectionState int

const (
	MQTTStateDisconnected ConnectionState = iota
	MQTTStateConnecting
	MQTTStateConnected
	MQTTStateReconnecting
)

// String returns the string representation of ConnectionState
func (s ConnectionState) String() string {
	switch s {
	case MQTTStateDisconnected:
		return "disconnected"
	case MQTTStateConnecting:
		return "connecting"
	case MQTTStateConnected:
		return "connected"
	case MQTTStateReconnecting:
		return "reconnecting"
	default:
		return "unknown"
	}
}

// BrokerInfo holds information about a connected broker
type BrokerInfo struct {
	Name      string // "custom", "us", "eu"
	Connected bool
	URL       string
}

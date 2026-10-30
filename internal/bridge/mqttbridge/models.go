package mqttbridge

import (
	"time"
)

// ============================================================================
// Device Info (passed from meshcore client to bridge)
// ============================================================================

// DeviceInfo contains device information used by the MQTT bridge for publishing.
// Populated from SelfInfo received from the MeshCore device.
type DeviceInfo struct {
	PublicKey       []byte
	Name            string
	TxPower         int8
	Latitude        float64
	Longitude       float64
	FreqHz          uint32
	BwHz            uint32
	Sf              int
	Cr              int
	FirmwareVersion string
	Model           string
}

// DeviceStats contains device statistics used by the MQTT bridge for publishing.
// Populated from CompanionRadioStats received from the MeshCore device.
type DeviceStats struct {
	// Core stats
	BatteryMv  uint16
	UptimeSecs uint32
	Errors     uint16
	QueueLen   uint8

	// Radio stats
	TxAirSecs     uint32
	RxAirSecs     uint32
	NoiseFloorDbm int8
	LastRssi      int8
	LastSnrDb     float32

	// Packet stats
	PacketsRecv       uint32
	PacketsSent       uint32
	PacketsFloodTx    uint32
	PacketsDirectTx   uint32
	PacketsFloodRx    uint32
	PacketsDirectRx   uint32
	PacketsRecvErrors uint32
}

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
	IATA string `yaml:"iata"` // IATA airport code (e.g., "SEA", "LAX")

	// Message settings
	StatusEnabled  bool `yaml:"status_enabled"`  // Enable status messages
	PacketsEnabled bool `yaml:"packets_enabled"` // Enable packet messages
	RawEnabled     bool `yaml:"raw_enabled"`     // Enable raw messages
	TxEnabled      bool `yaml:"tx_enabled"`      // Enable TX packet messages

	// PublishChannelKeys controls the channel_key field on published packets:
	// the channel's 16-byte PSK, which decrypts all of that channel's traffic,
	// past and future. Off by default — enabling hands the key to the analyzer
	// servers and to every subscriber of the packets topic. Opt in only for
	// channels whose content you accept making public to whoever runs or reads
	// the analyzer.
	PublishChannelKeys bool `yaml:"publish_channel_keys"`

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
		// PublishChannelKeys deliberately stays false: publishing a channel's
		// PSK discloses the ability to decrypt it, so it must be opt-in.
		PublishChannelKeys: false,
		StatusInterval:     300000, // 5 minutes
		AnalyzerUS:         true,
		AnalyzerEU:         true,
		Port:               1883,
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
	SNR        string `json:"SNR,omitempty"`  // Signal-to-noise ratio (dB); omitted for TX
	RSSI       string `json:"RSSI,omitempty"` // Received signal strength (dBm); omitted for TX
	Hash       string `json:"hash"`           // Packet hash (32 hex)
	Path       string `json:"path,omitempty"` // Routing path for direct packets

	// Decoded channel message fields — present only when the raw packet was
	// identified as a channel message and correlated with the 0x11 channel message frame.
	ChannelIndex *int   `json:"channel_index,omitempty"` // Channel number (0-based)
	ChannelName  string `json:"channel_name,omitempty"`  // Channel display name
	SenderName   string `json:"sender_name,omitempty"`   // Sender display name
	Text         string `json:"text,omitempty"`          // Decrypted message text
	ChannelKey   string `json:"channel_key,omitempty"`   // 32 hex chars (16-byte PSK); lets analyzer servers decrypt unknown channels
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

	// Decoded message fields — populated when a 0x88 raw frame is correlated
	// with the 0x11 channel message frame from the same radio packet.
	IsChannelMessage bool
	ChannelIndex     int
	ChannelName      string
	SenderName       string
	Text             string
	ChannelKey       string // hex-encoded 16-byte PSK; lets the analyzer server decrypt without config
}

// MQTTCallbacks holds callback functions for MQTT events
type MQTTCallbacks struct {
	OnConnected    func(string) // broker name
	OnDisconnected func(string) // broker name
}

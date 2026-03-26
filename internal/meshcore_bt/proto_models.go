package meshcore_bt

import "time"

// ============================================================================
// Типы данных
// ============================================================================

type Contact struct {
	PublicKey         []byte
	Name              string
	Type              int
	Flags             int
	PathLength        int
	Path              []byte
	Latitude          *float64
	Longitude         *float64
	LastSeen          time.Time
	LastMessageAt     time.Time
	IsActive          bool
	PathOverride      *int // -1 = flood, nil = auto, >=0 = specific length
	PathOverrideBytes []byte
}

type Channel struct {
	Index       int
	Name        string
	PSK         []byte // поле для ключа канала
	UnreadCount int
	IsEmpty     bool
}

type Message struct {
	SenderKey  []byte
	Text       string
	Timestamp  time.Time
	IsOutgoing bool
	IsCli      bool
	Status     string // "pending", "sent", "delivered", "failed"
	PathLength int
	PathBytes  []byte
	MessageID  string
}

type ChannelMessage struct {
	ChannelIndex int
	SenderName   string
	Text         string
	Timestamp    time.Time
	IsOutgoing   bool
	Status       string
	PathLength   int
	PathBytes    []byte
	MessageID    string
}

type CompanionRadioStats struct {
	TxAirSecs     uint32
	RxAirSecs     uint32
	NoiseFloorDbm int8
	LastSnrDb     float32
}

type SelfInfo struct {
	PublicKey       []byte
	Name            string
	TxPower         int8
	MaxTxPower      int8
	Latitude        float64
	Longitude       float64
	MultiAcks       int
	AdvertLocPolicy int
	TeleModeBase    int
	TeleModeLoc     int
	TeleModeEnv     int
	ManualAdd       bool
	FreqHz          uint32
	BwHz            uint32
	Sf              int
	Cr              int
	ClientRepeat    bool
}

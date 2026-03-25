package meshcore_bt

// ============================================================================
// Константы протокола
// ============================================================================

const (
	serviceUUID          = "6e400001-b5a3-f393-e0a9-e50e24dcca9e"
	rxCharacteristicUUID = "6e400002-b5a3-f393-e0a9-e50e24dcca9e"
	txCharacteristicUUID = "6e400003-b5a3-f393-e0a9-e50e24dcca9e"
)

// Command codes (to device)
const (
	cmdAppStart          = 0x01
	cmdSendTxtMsg        = 0x02
	cmdSendChannelTxtMsg = 0x03
	cmdGetContacts       = 0x04
	cmdSetDeviceTime     = 0x06
	cmdSendSelfAdvert    = 0x07
	cmdSetAdvertName     = 0x08
	cmdAddUpdateContact  = 0x09
	cmdSyncNextMessage   = 0x0A
	cmdResetPath         = 0x0D
	cmdSetAdvertLatLon   = 0x0E
	cmdRemoveContact     = 0x0F
	cmdReboot            = 0x13
	cmdGetBattAndStorage = 0x14
	cmdDeviceQuery       = 0x16
	cmdGetContactByKey   = 0x1E
	cmdGetChannel        = 0x1F
	cmdSetChannel        = 0x20
	cmdSetOtherParams    = 0x26
	cmdGetCustomVar      = 0x28
	cmdSetCustomVar      = 0x29
	cmdSetAutoAddConfig  = 0x3A
	cmdGetAutoAddConfig  = 0x3B
	cmdSetPathHashMode   = 0x3D
	cmdGetStats          = 0x38
)

// Response codes (from device)
const (
	respCodeOk               = 0x00
	respCodeErr              = 0x01
	respCodeContactsStart    = 0x02
	respCodeContact          = 0x03
	respCodeEndOfContacts    = 0x04
	respCodeSelfInfo         = 0x05
	respCodeSent             = 0x06
	respCodeContactMsgRecv   = 0x07
	respCodeChannelMsgRecv   = 0x08
	respCodeNoMoreMessages   = 0x0A
	respCodeBattAndStorage   = 0x0C
	respCodeDeviceInfo       = 0x0D
	respCodeContactMsgRecvV3 = 0x10
	respCodeChannelMsgRecvV3 = 0x11
	respCodeChannelInfo      = 0x12
	respCodeCustomVars       = 0x15
	respCodeAutoAddConfig    = 0x19
	respCodeStats            = 0x18
)

// Push codes (async from device)
const (
	pushCodeAdvert        = 0x80
	pushCodePathUpdated   = 0x81
	pushCodeSendConfirmed = 0x82
	pushCodeMsgWaiting    = 0x83
	pushCodeLogRxData     = 0x88
)

// Text message types
const (
	txtTypePlain   = 0x00
	txtTypeCliData = 0x01
	txtTypeSigned  = 0x02
)

// Contact/advertisement types
const (
	advTypeChat     = 1
	advTypeRepeater = 2
	advTypeRoom     = 3
	advTypeSensor   = 4
)

// Contact flags
const (
	contactFlagFavorite = 0x01
	contactFlagTeleBase = 0x02
	contactFlagTeleLoc  = 0x04
	contactFlagTeleEnv  = 0x08
)

// Auto-add flags
const (
	autoAddOverwriteOldest = 1 << 0
	autoAddChat            = 1 << 1
	autoAddRepeater        = 1 << 2
	autoAddRoomServer      = 1 << 3
	autoAddSensor          = 1 << 4
)

// Telemetry modes
const (
	teleModeDeny       = 0
	teleModeAllowFlags = 1
	teleModeAllowAll   = 2
)

// Размеры
const (
	pubKeySize         = 32
	signatureSize      = 64
	maxPathSize        = 64
	maxNameSize        = 32
	maxFrameSize       = 172
	appProtocolVersion = 3
	contactFrameSize   = 148 // Размер фрейма контакта
)

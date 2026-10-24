package registry

import (
	"strings"
	"time"

	"meshbot/internal/config"
	"meshbot/internal/support/i18n"
)

// PingMeta holds path, timing, and location metadata from the incoming message.
// It lives here so both the dispatch layer (which builds it) and the command
// plugins (which read it) reference one definition.
type PingMeta struct {
	PathLength int    // hop count from message wire header (0xFF = direct delivery)
	PathBytes  []byte // path node IDs from sender's contact (may be nil for channel messages)
	Timestamp  time.Time
	SenderLat  *float64     // sender GPS latitude (nil if unknown)
	SenderLon  *float64     // sender GPS longitude (nil if unknown)
	SelfLat    float64      // own GPS latitude (0 if unknown)
	SelfLon    float64      // own GPS longitude (0 if unknown)
	HopCoords  [][2]float64 // lat/lon for each intermediate hop; nil entry slot = unknown position
	HopKnown   []bool       // true when HopCoords[i] is valid
}

// Ctx is the unified handler input, replacing the divergent per-command
// signatures of the old commands package.
type Ctx struct {
	Text       string         // raw incoming text
	Trimmed    string         // strings.TrimSpace(Text)
	Lower      string         // strings.ToLower(Trimmed)
	Channel    string         // channel name; "" for DMs
	SenderKey  string         // stable per-sender id (see dispatch for encoding)
	SenderName string         // human-readable author; may be ""
	PingMeta   *PingMeta      // path/timing/location metadata
	Cfg        *config.Config // shared/infra config (read-only)
	Lang       string         // resolved bot-wide UI language (see NewCtx); handlers key i18n lookups on this
}

// NewCtx builds a Ctx, filling Trimmed/Lower from Text.
func NewCtx(text, channel, senderKey, senderName string, pm *PingMeta, cfg *config.Config) Ctx {
	t := strings.TrimSpace(text)
	lang := i18n.Base
	if cfg != nil && cfg.Language != "" {
		lang = cfg.Language
	}
	return Ctx{
		Text:       text,
		Trimmed:    t,
		Lower:      strings.ToLower(t),
		Channel:    channel,
		SenderKey:  senderKey,
		SenderName: senderName,
		PingMeta:   pm,
		Cfg:        cfg,
		Lang:       lang,
	}
}

// Arg returns the argument after a command prefix: TrimSpace(TrimPrefix(Trimmed, prefix)).
func (c Ctx) Arg(prefix string) string {
	return strings.TrimSpace(strings.TrimPrefix(c.Trimmed, prefix))
}

// IsDM reports whether this message arrived as a direct message.
func (c Ctx) IsDM() bool { return c.Channel == "" }

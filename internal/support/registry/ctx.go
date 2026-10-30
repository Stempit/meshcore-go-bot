package registry

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stempit/meshcore-go-bot/internal/config"
	"github.com/stempit/meshcore-go-bot/internal/support/i18n"
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
	Text    string // raw incoming text
	Trimmed string // strings.TrimSpace(Text)
	Lower   string // strings.ToLower(Trimmed)
	// Rest is the remainder after the trigger Dispatch matched, trimmed of
	// surrounding space (".path TIM to CHEP" → "TIM to CHEP"). Dispatch fills
	// it for trigger-matched commands; Matcher interceptors leave it empty and
	// cut their own prefixes with Arg.
	Rest       string
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

// Arg returns the argument after a command prefix, following the registry's
// Prefix rule: the prefix must be followed by a space or end the message
// (".W moscow" and ".w moscow" both yield "moscow"; Arg(".w") on ".words x"
// yields "" instead of slicing mid-word). The cut is rune-aligned because
// ToLower preserves rune count but not byte length. Returns "" when the text
// does not start with the prefix — it deliberately does not fall back to the
// whole text, which the old case-sensitive TrimPrefix silently did on
// ".W city", producing garbage args.
func (c Ctx) Arg(prefix string) string {
	p := strings.ToLower(prefix)
	if !strings.HasPrefix(c.Lower, p) {
		return ""
	}
	if rest := c.Lower[len(p):]; rest != "" && !strings.HasPrefix(rest, " ") {
		return ""
	}
	r := []rune(c.Trimmed)
	n := utf8.RuneCountInString(p)
	if len(r) < n {
		return ""
	}
	return strings.TrimSpace(string(r[n:]))
}

// IsDM reports whether this message arrived as a direct message.
func (c Ctx) IsDM() bool { return c.Channel == "" }

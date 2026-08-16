# helpers — small cross-command utilities

Package `helpers` holds tiny utilities shared across command plugins, so each command
folder doesn't re-declare the same constant or clipping logic.

## Contents

- **`HistoryDir`** — the base directory (`log/history`) the `.last` / `.top` / `.stat`
  readers scan for channel and DM message history (written by the bot's history logger).
- **`ClipRunes(s, maxRunes)`** — shorten a string to at most `maxRunes` **runes** (not
  bytes), appending an ellipsis when it had to cut. Counting runes keeps multi-byte
  Cyrillic names within the 140-byte mesh message limit.

## Configuration

None.

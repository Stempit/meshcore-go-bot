# games — shared support layer for text games

Package `games` is the **shared support layer** for the text-game command plugins
([`.dice`](../../commands/dice/README.md), [`.num`](../../commands/num/README.md),
[`.hang`](../../commands/hang/README.md), [`.words`](../../commands/words/README.md)).
The game logic lives in each command folder; this package holds only what those plugins
share. It imports no command package.

## What it provides

- **Scope keying** — `Scope(senderKey, channelName)`: the shared game-scope string,
  `channelName` for channels or `"dm:" + senderKey` for DMs. This is how one mechanic
  serves both shared (channel) and private (DM) games.
- **Leaderboards** — win records appended (via `historylog`) under
  `log/games/<game>/channels/<scope>.txt`, with helpers to log a win and to render a
  30-day, wins-per-player, ranked, top-5 `.<game> top` line (byte-budgeted for the mesh
  limit).
- **Announcer** — an injected `Announcer func(scope, msgs)` for pushing unsolicited
  round results (used by `.words` and `.vote` timers). It is a no-op until
  `SetAnnouncer` is called at startup from `cmd/bot` with `dispatch.AnnounceToScope`,
  which resolves a scope back to a channel/contact. Decoupled: `games` exposes a func
  type and never imports `dispatch`/`meshcore`.
- **`NormalizeRu`** — lowercase, `ё→е`, keep only `а–я` (shared by the word games).
- **`NormalizeEn`** — lowercase, keep only `a–z` (English variant, shared by the word games).

`BaseDir` (`log/games`) is the root for per-game result logs.

## Configuration

None of its own. Each game's config lives in its command folder; win logs go under
`log/games/`.

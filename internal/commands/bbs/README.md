# bbs — forum / BBS mode

Package `bbs` registers the `.2ch` command: a stateful, per-sender forum ("BBS") mode.
Once a sender enters BBS mode it claims **all** of that sender's input until they
`.exit`, so plain messages become BBS sub-commands rather than normal bot commands.

`bbs` is a `Matcher` interceptor at **Priority 10** — above normal commands (0) but
below agent (20), so an `@[BotName]` mention still beats BBS mode.

## Command surface

Enter with `.2ch`; leave with `.exit`. While inside:

| Sub-command | Action |
|-------------|--------|
| `/hl` | Help |
| `/ls [page]` | List threads (8 per page, newest activity first) |
| `/rd <id> [page]` | Read a thread (3 posts per page) |
| `/nw <title>\|<text>` | New thread |
| `/rp <id>\|<text>` | Reply to a thread |

Replies are packed to ≤138 chars per message; long posts are split at the send site.
Author display is the username (clipped to 8) or the first 6 hex chars of the key.

## Persistence

Threads are stored as JSON in `log/2ch/threads.json`, loaded lazily on first use and
saved on every new thread/reply. Per-sender BBS-mode membership is in-memory (lost on
restart). Session state is keyed by `SenderKey`.

## Configuration

None (paths are fixed in code).

# .poker — fixed-limit Texas Hold'em on a MeshCore channel

A public channel hosts one table (2–6 players); the bot is the dealer and
commentator. The channel carries the public show — management commands, board,
betting news, results. **All betting actions happen in the bot's DMs**, where
each player also sees their hole cards and a personal event log (`.poker log`).

Full design and decisions: `dist/poker-plan.md` in the repo root.

## Commands

Channel:

- `.poker` — one-line spectator status (players / street / pot / whose turn)
- `.poker start` — open a table (60s join window; the opener seats too)
- `.poker join` — sit, or queue for the next hand during play
- `.poker leave` — exit after the current hand
- `.poker end` — close the table now (**opener only**; a seated non-opener
  running it is treated as `leave`; once the opener has left, any seated
  player may close)
- `.poker top` — 30-day win leaderboard for the channel

DM (a player may sit at one table bot-wide, so DM actions are unambiguous):

- `call` / `check` / `fold` / `bet` / `raise` / `allin` — bare, `.call`, or
  `.poker call` all work; amounts are accepted and ignored (fixed limit —
  the engine knows the sizes). Turn prompts are **dynamic and priced**:
  only the legal actions with absolute prices (`call 10 / raise до 20 / fold`,
  `check / raise до 10 / fold`, `call 137 (все)` when short).
- `.poker` — personal status; `.poker cards` — re-send your hand;
  `.poker log` — your last ~10 personal events; `.poker top`.

An action verb typed on the channel gets guidance only (`Ходы — только в
личке 🤫`), no state change.

## Rules

Fixed-limit: blinds 5/10 (small_bet/2, small_bet), small bet (10) preflop and
flop, big bet (20) turn and river, one raise = one full increment, max 3
raises per street (an all-in short of a full raise is allowed but does not
reopen the cap accounting). Heads-up: the button posts the SB and acts first
preflop. Side pots split by contribution levels (uncalled bets refunded
first); split pots divide evenly, the odd chip going to the first winner left
of the button. Showdown names every live hand (`P0 — пара A`); the win and
split lines additionally reveal the **winners'** hole cards (losers' cards
stay hidden). The deck is shuffled with crypto/rand.

## Table life

60s join window (deals early at 6 players); 90s turn timeout with auto
check-else-fold; **two consecutive auto-actions (check or fold, across hands)
remove a player** — someone who misses two turns in a row is gone or not
playing; 20s deal-pause window between hands (queued joiners seat then);
15-minute idle closes the table; last chipholder wins (`games.LogWin`, shown
via `.poker top`).

## Limitations

- State is in-memory: a restart drops any running table (repo convention).
- One table per channel; one table per player bot-wide.
- Joining requires the bot to know your contact (advert exchanged): the
  channel path resolves the sender by display name, so duplicate contact
  names can confuse it, and unknown senders get the pinned
  `Не получилось, нет твоего адверта`.
- Dealer lines pushed by timers are not forwarded to the Telegram bridge
  (same as `.vote`/`.words` announcements); direct command replies are.
- A seated player inside BBS mode (`.2ch`) has their DM actions swallowed by
  the BBS interceptor until `.exit`; the turn timeout still protects them.
- On busy channels, consider a `Redirect` rule for `poker` (see CLAUDE.md).

## Config

See the `poker:` section in `config.example.yaml` — opt-in via
`poker.enabled: true`; blinds/bets, stack, timers, and table size are all
tunable.

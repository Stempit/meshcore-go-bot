# support

The shared framework and support layer sitting **above** `commands/`. These packages are
imported by command plugins; they never import a specific command.

| Package | What it is |
|---------|------------|
| [`registry/`](registry/README.md) | the command framework: `Ctx`, `Meta`, `Command`, `Register`, `Dispatch`, help, redirect gate |
| [`games/`](games/README.md) | shared support for the text-game plugins: scope keying, leaderboards, the round-result `Announcer`, `NormalizeRu` |
| [`helpers/`](helpers/README.md) | small cross-command utilities (`HistoryDir`, `ClipRunes`) |
| [`historylog/`](historylog/README.md) | per-conversation append-only message logs (backs `.last`/`.top`/`.stat` and game leaderboards) |

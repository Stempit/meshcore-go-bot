# alerts — hazard-alert poller

Package `alerts` polls one or more hazard-alert sources and calls an injected `OnEvent`
callback for each newly-published item, so it can be relayed to a MeshCore channel. Like
`tgbridge`, it has **no dependency on meshcore/dispatch** — delivery is the caller's
job.

## What it does

Each configured source runs its own poll loop on its own ticker. A source is one of two
types:

- **`html_cards`** — bplarussia.ru-style FPV/UAV incident cards (`parse_bplarussia.go`).
- **`rss`** — a standard RSS feed (`rss.go`).

On each poll the bridge fetches the current items (oldest-first), filters by an optional
`title_prefix`, records new ids in a per-source seen-set, and announces the new ones
(oldest first, capped at `max_per_poll`). Announced text is **title only** — no
description — to stay within the mesh's tight per-message byte limit.

**First-run seeding:** if a source's seen-set is empty (fresh install), the first poll
silently seeds it from whatever is already published instead of announcing, so the
channel isn't flooded with history. Dedup state lives in `log/alerts/<name>.txt`.

## Configuration

YAML key `Alerts`, holding a list of independent `sources`, each opt-in via its own
`enabled` flag (default false):

```yaml
Alerts:
  sources:
    - enabled: true
      name: bpla                 # logging + seen-file name (log/alerts/bpla.txt)
      type: html_cards           # html_cards | rss
      url: "https://bplarussia.ru/..."
      channel: "#alerts"         # MeshCore channel — must already exist on the device
      poll_interval_sec: 60      # default 60
      max_per_poll: 3            # default 3
      postfix: " #БПЛА"          # optional: appended as-is (include your own separator)
    - enabled: true
      name: mchs
      type: rss
      url: "https://..."
      channel: "#alerts"
      title_prefix: "Внимание"   # optional: keep only items whose title starts with this
```

`title_prefix` on the shipped `mchs` source keeps only genuine МЧС hazard warnings and
skips routine daily reports.

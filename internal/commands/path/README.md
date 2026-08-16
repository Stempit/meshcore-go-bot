# path — best route between repeaters

Package `path` registers the `.path` command: a network summary, or the best route
between two nodes, computed from the Let's Mesh analyzer edge-stats data source.

## Command surface

```
.path                    — network summary (node/edge counts)
.path <from> to <to>     — best path between two nodes
.path <A> <B>            — shorthand when both names are single words
```

Nodes are resolved in order: exact name (case-insensitive) → public-key hex prefix
(2+ hex chars) → partial name substring. An ambiguous query returns the candidate list.

## How path selection works

The algorithm maximises **end-to-end delivery probability**, not minimum hop count.
Each link gets a cost of `−log(P_hop)`; Dijkstra minimises total cost, i.e. maximises
the product of per-hop probabilities. Per-hop probability combines three signals:

| Signal | Model | Captures |
|--------|-------|----------|
| SNR avg | Sigmoid around the −5 dB demod floor | Demodulation quality |
| RSSI avg | Sigmoid around the −110 dBm noise floor | Weak-signal frame loss |
| link_count | Log-saturation | Long-term empirical reliability |

When RF measurements exist, radio quality carries 70% (geometric mean of the two
sigmoids) and link activity 30%; with no RF data, link_count is used alone.

The reply labels the path by its **worst SNR hop** (`strong`/`ok`/`weak`/`marginal`/`?`)
and shows an estimated delivery percentage, e.g.:

```
3 hop(s) [ok ~62%]: Ippodrom South→TIM→Repeater GRIN→CHEP
```

If the route string exceeds 130 chars, node names are abbreviated to their first word.

## Configuration

YAML key `Stats` — the analyzer edge-stats source this plugin reads. (Note: this is the
`stats.json` source owned by `.path`, **not** the unrelated `.stat` channel-traffic
command — the name clash is a trap.)

```yaml
Stats:
  url: "file:///path/to/stats.json"   # HTTP(S) URL or file:// snapshot
  cache_ttl_sec: 300                   # refresh interval; 0 = default 5 min
```

The `file://` scheme accepts a local snapshot for testing. The JSON format is the
Let's Mesh analyzer edge-stats API response.

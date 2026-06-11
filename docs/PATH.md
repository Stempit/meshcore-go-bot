# `.path` command — path finding algorithm

## Command syntax

```
.path                          — network summary (node/edge counts)
.path <from> to <to>           — best path between two nodes
.path <A> <B>                  — shorthand when both names are single words
```

Nodes can be identified three ways (tried in order):

1. **Exact name** — case-insensitive, e.g. `TIM` or `tim`
2. **Public-key hex prefix** — 1 or more bytes (2+ hex chars), e.g. `D54D` or `d54d562a`
3. **Partial name substring** — case-insensitive contains match, e.g. `grin`

If a query matches more than one node the bot replies with the list of
candidates so you can narrow it down.

## How path selection works

The algorithm maximises **delivery probability** rather than minimising hop
count. Each link is assigned a cost of `−log(P_hop)`, and Dijkstra finds the
path that minimises total cost — equivalent to maximising the product
`P_hop1 × P_hop2 × …`.

### Per-hop delivery probability

Three independent signals are combined into a single probability estimate for
each edge:

| Signal | Model | What it captures |
|---|---|---|
| **SNR avg** | Sigmoid around the LoRa demodulation floor (−5 dB): `1/(1+exp(−(snr+5)/3))` | Physical demodulation quality |
| **RSSI avg** | Sigmoid around the noise floor (−110 dBm): `1/(1+exp(−(rssi+110)/8))` | Frame loss risk from weak signal |
| **link_count** | Log-saturation: `1 − 1/(1+log₂(1+N))` | Empirical long-term reliability |

When RSSI/SNR measurements are present, radio quality carries **70 %** of the
weight (geometric mean of the two sigmoid values) and link activity carries
**30 %**. When no RF measurements exist for an edge, link_count is used alone.

### Quality labels

The reply includes a label based on the **worst SNR hop** on the path:

| Label | Minimum SNR |
|---|---|
| `strong` | ≥ +5 dB |
| `ok` | 0 … +5 dB |
| `weak` | −5 … 0 dB |
| `marginal` | < −5 dB |
| `?` | no SNR data available |

The estimated end-to-end delivery percentage is also shown (e.g. `~62%`).

### Example output

```
3 hop(s) [ok ~62%]: Ippodrom South→TIM→Repeater GRIN→CHEP
```

If the route string exceeds 130 characters (mesh radio message limit), node
names are automatically abbreviated to their first word.

## Configuration

```yaml
Stats:
  url: "file:///path/to/stats.json"   # or an HTTPS API endpoint
  cache_ttl_sec: 300                   # refresh interval; 0 = default 5 min
```

The `file://` scheme accepts a local snapshot for testing (e.g. the
`stats2.json` file in the project root). The JSON format is the Let's Mesh
analyzer edge-stats API response.

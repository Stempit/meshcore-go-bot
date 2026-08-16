# trace — hop count & distance

Package `trace` registers the `.trace` command: reports the delivery path and distance
of the sender's last ping, from the `registry.PingMeta` attached to the request.

## Command surface

| Trigger | Match | Reply |
|---------|-------|-------|
| `.trace` | exact | hop count + hop IDs + distance |

Example replies:

- `Pong direct` — `PathLength == 0xFF` (delivered directly, no repeaters).
- `Pong 2 hops: [A1][B2] (5s) 3.4km` — hop IDs from `PathBytes` (auto-detecting 1- or
  2-byte-per-hop encoding), age since the ping, and Haversine distance.

Distance is the great-circle distance between this node's coordinates and the sender's.
If the sender's coordinates are unknown it falls back to the first repeater's position
and marks the distance with `+` (a minimum estimate). When every intermediate hop has
known coordinates, it also reports the total path length (`direct->total`).

## Configuration

Reads the central `Trace` config (this node's coordinates), decoded in the dispatch
layer while building `PingMeta` — see [`internal/config`](../../config/README.md):

```yaml
Trace:
  use_config_coords: true
  lat: 55.75
  lon: 37.62
```

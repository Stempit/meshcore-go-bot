# ping — random pong

Package `ping` registers the `.ping` command: answers with one random "pong" string
from a large built-in list (mostly playful Russian one-liners). It's the basic
liveness/latency check over the mesh.

## Command surface

| Trigger | Match | Reply |
|---------|-------|-------|
| `.ping`, `ping`, `пинг` | exact | a random pong string |

The three triggers are equivalent. The reply text is ignored on input — any of the
triggers just returns a random line.

## Configuration

None.

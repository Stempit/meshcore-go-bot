---
name: channel-msg-ok-not-delivery
description: Why channel messages log as "sent" but never reach the mesh, and the repeat-send fix
metadata:
  type: project
---

Channel (group) messages have **no delivery ACK**. The MeshCore firmware's `respCodeOk`
is written by `BaseChatMesh::sendGroupMessage` the moment the packet is created and
**queued** (`writeOKFrame` in companion_radio `MyMesh.cpp` CMD_SEND_CHANNEL_TXT_MSG) —
NOT when it is transmitted on RF. After OK, the Dispatcher can still silently drop the
queued packet: TX duty-cycle budget exhausted (`checkSend` `tx_budget_ms`), channel-busy
CAD, `startSendRaw` failure (→ `logTxFail`), or a full outbound queue
(`StaticPoolPacketManager::queueOutbound` "send queue full, dropping packet"). None of
these notify the companion app, and the firmware's seen-hash dedup hides RF rebroadcasts
of our own packet. So "Channel reply sent" in the bot logs is a weak guarantee.

**Why the old retry loop didn't help:** OK arrives reliably on attempt 0, so
`SendChannelMessageWithRetry`'s reject-retry never fired while the packet was lost on air.

**Fix (matches the official Android app, which visibly repeats channel sends):** transmit
the SAME packet N times reusing ONE timestamp. `Packet::calculatePacketHash` is SHA256 over
payload (which begins with the 4-byte timestamp), so identical timestamp+text ⇒ identical
packet_hash ⇒ receivers dedup and show it once, while each copy is an independent chance to
get on air. The bot's own `sendFlood` marks-then-sends, so it only dedups inbound, never our
outbound resends.

Implemented: `SendChannelMessageWithRetry(channel, text, ack, maxRetries, repeats, timeout)`
in [internal/meshcore/client.go](internal/meshcore/client.go) (new `repeats` param +
extracted `sendChannelOnce`, `sendChannelTextMessageTS`). Config:
`Connection.channel_send_repeats` (default 3, 1 disables). Firmware reference lives at
./MeshCore.

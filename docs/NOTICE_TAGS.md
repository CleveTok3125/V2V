# Notice Tags

Status: implemented. Every server-originated notice, every replay window
marker and every local informational line carries the tags that apply to
it, and the client decides what to show from those tags. The wire field
this replaced, `sys_kind`, is gone and there is no compatibility with
it — see [Compatibility](#compatibility).

## Goals

- One decision, made once. The server knows what a line *is*; the client
  must not reach the same conclusion by reading the wording a second time.
  A reworded banner stays the same banner, and a copy edit cannot silently
  change which tab a line lands in or which switch mutes it.
- Extensible without a vocabulary change. A new notice kind needs one
  tag constant and a producer. No config field, no `/notify` case, no
  web row — the taxonomy is data, and both UIs render it.
- Hierarchical, so a switch is a subtree. Muting `system.pow` hides the
  proof-of-work flow without touching the rest; muting `system` hides
  everything, which is the one global off switch.
- Lossless. A muted line still lands in the system tab. Muting changes
  what reaches the terminal, never what the user can go back and read.

## Terminology

- **Notice** — a system line the client may hide: a server broadcast, a
  replay marker, or a local informational message. Carries tags.
- **Tag** — a dotted path from the root, e.g. `system.pow.screening`.
  Every tag a line carries is a full path, never a bare leaf name.
- **Chain** — a tag plus every tag enclosing it. A line publishes the
  whole chain, so `system.pow.screening` travels as
  `["system", "system.pow", "system.pow.screening"]`.
- **Muted tag** — a node the user turned off. Muting a node covers its
  whole subtree.
- **Gating** — hiding a line from the live terminal. Unrelated to which
  tab it is buffered into.
- **Routing** — which tab a rendered line belongs to. Also decided by
  tags, never by wording.

## Tag tree

Declared in `internal/wire/tags.go`; `AllTags()` returns it in tree order
and is what `/notify` and the web settings panel render.

| Tag | Covers | Produced by |
| --- | --- | --- |
| `system` | every notice | (root) |
| `system.join` | someone joined | `server/clientHandler.go` |
| `system.leave` | someone left | `server/clientHandler.go` |
| `system.date` | the daily banner | `server/history.go` (`BroadcastDate`) |
| `system.history` | replay window markers | `server/history.go` |
| `system.history.begin` | catch-up window header | `replayHeader(replayJoin)` |
| `system.history.older` | on-demand older-segment header | `replayHeader(replaySegment)` |
| `system.history.recover` | recovery window header | `replayHeader(replayRecovery)` |
| `system.history.end` | counted footer (`sent/total`) | `sendReplayPage` |
| `system.history.exhausted` | footer: no older lines left | `sendReplayPage` |
| `system.pow` | the proof-of-work flow | (intermediate) |
| `system.pow.gate` | chat held pending a pre-connect proof | `server/clientHandler.go` |
| `system.pow.screening` | in-chat challenge, solved, rejected, declined | `server/powscreen.go` |
| `system.limit` | rate limit, slowmode, too fast, too long, too many lines | `server/clientHandler.go` |
| `system.envelope` | bad protocol: missing `tmp_id`, unknown reply target, empty text, obsolete format | `server/clientHandler.go` |
| `system.trip` | trip structure: no content, pubkey mismatch, bad signature, wrong seq, broken prev, unsigned | `server/clientHandler.go` |
| `system.filter` | rejected characters | `server/clientHandler.go` |
| `system.auth` | tripcode too long, concurrent identity login | `server/auth.go` |
| `system.audit` | reserved: a management line chained as evidence | **none yet** |

Two entries deserve their own note. `system.audit` has a route
(`Hub.BroadcastAudit`) but no producer, so no line carries the tag today
and muting it changes nothing; it stays in the tree so the wire shape is
settled before anything publishes on it. `system.history` is an
intermediate node with no producer of its own — it exists so
`/notify system.history off` is one switch rather than five.

## Wire format

```jsonc
{
  "type": "system",
  "time": "00:00",
  "tags": ["system", "system.pow", "system.pow.screening"],
  "text": "[Hệ thống]: Xác minh PoW xong, chat mở lại bình thường."
}
```

`tags` is omitted when empty (`omitempty`); `text` keeps the
`[Hệ thống]: ` prefix, which is display only — nothing parses it.

A date banner also carries `sys_date`, the announced day as
`2006-01-02`:

```jsonc
{
  "type": "system",
  "time": "00:00",
  "tags": ["system", "system.date"],
  "sys_date": "2026-09-27",
  "text": "\u001b[36m--- Ngày 27/09/2026 ---\u001b[0m"
}
```

A proof-of-work challenge notice carries its tier the same way:

```jsonc
{
  "type": "system",
  "tags": ["system", "system.pow", "system.pow.screening"],
  "sys_pow_tier": 3,
  "text": "[Hệ thống]: Máy chủ yêu cầu xác minh chống spam (mức PoW 3). …"
}
```

Without the field the tier would exist only inside the banner text, and
nothing parses that, so `ui.powMinTier` could not act on a notice the
server sent. Only the challenge carries one: a deadline, a result or a
decline has no tier, and those are never filtered by the floor, which says
how much work to announce rather than which outcome to hide.

The banner text alone is a pre-rendered string. Without a machine value
next to it, a client that already showed a day — from the replay tail, or
from a server that restarted and re-announced it — could only recognise
the repeat by comparing the text it had already drawn, which any change to
the wording would defeat. `sys_date` is why the dedup is a date
comparison rather than a string one.

Tags and `sys_date` are display metadata. They are not covered by the
chain hash, and a peer can strip or forge them; they affect what a client
shows, not whether a line verifies.

### Three delivery routes

- `Hub.BroadcastWire` — chat. Chained.
- `Hub.BroadcastAudit` — management lines that must serve as evidence.
  Chained. No producers yet; use it (never the notice path) for a future
  ban/kick/mute/rolechange.
- `unicastNotice` / `Hub.BroadcastNotice` / `Hub.BroadcastDate` — notices
  and markers. Unchained: they carry no authorship or ordering evidence.
  The first two are per-client and live-only, so they never enter a
  replay.

## Hierarchy semantics

A line prints live **iff every tag it carries, and every ancestor of those
tags, is shown**. Muting any one hides it. `wire.BlockedBy` is the whole
rule, and it is the only place it lives.

Producers name a leaf; `wire.WithTags` expands it to the full chain, so
the rule cannot be half-applied by forgetting a parent. Consumers ask
whether a line is blocked rather than testing one key at a time, which is
what makes a muted root reach a leaf it does not itself name.

An unknown level does not detach a line from the tree. A peer on a newer
vocabulary publishing `system.pow.newflow.step` still carries
`system.pow`, so muting the proof-of-work flow still hides it:
`wire.knownAncestor` walks past levels this build does not know to the
nearest one it does, and the root is always tested.

## Client filtering

- **Routing** — `tabForWire` sends a chat line and a replay marker to
  Tab 1, and every other system line to Tab 2. A marker frames the chat
  history stream, so it belongs beside the lines it frames.
- **Gating** — `notifyTagsAllowed` asks `BlockedBy` whether any carried tag
  or ancestor is muted. A muted line is still appended to its tab, so it
  stays reviewable; only the live print is skipped.
- **Replay tracking** — `parseHistoryBoundary` reads the tags to decide
  whether a marker opens (`begin`, `older`, `recover`) or closes (`end`,
  `exhausted`) a window. That state machine also drives the fork check
  and the recovery refill, so it must not depend on wording.
- **Join/leave and date detection** — tag lookups on the wire. Untagged
  system content (from a peer that predates tagging) falls back to the
  root, so it is still covered by the system switch.

### There is deliberately no text fallback

The client does not match message text to decide anything, and no helper
was kept as a fallback. A second classifier is a second copy of a
decision the server already made, and the two drift: the wording changes,
the classifier does not, and the line is misrouted or wrongly muted with
nothing to indicate why.

The cost is that content arriving without tags is treated as generic.
An untagged system line is gated by `system` and routed to Tab 2, and a
frame that is not a wire at all is printed ungated in Tab 1 rather than
guessed at.

## Configuration

`ui.notify` is a map keyed by tag path, and `ui.powMinTier` sits beside
it:

```jsonc
"ui": {
  "notify": {"system.pow": false, "system.join": true},
  "powMinTier": 1
}
```

An absent key means **shown**, so a notice kind this build does not know
is not silenced by omission, and a partial config cannot mute a room by
accident. `powMinTier` (default 1) hides a proof-of-work notice for tiers
below it — the challenge the server announces, and the "solving PoW" line
the client prints about the same challenge. A notice with no tier is not
filtered by it.

The floor from config reaches every one of them. The floor set at runtime
with `/notify powmin <N>` reaches all but the pre-connect join-gate notice:
that one is printed by the dial which is about to create the session, so
there is no runtime state to read when it fires. `/notify` says so in its
listing and its confirmation. `TestPowMinTierScope` pins the split.

`/notify` (or `/nt`) lists the taxonomy as a tree and toggles a path:

```
system                    TẮT
  system.join             TẮT
  system.leave            TẮT
  system.pow              BẬT
```

Each row shows the state that actually applies, so a child of a muted
parent reads as off. `all on|off` flips everything, `powmin <N>` sets the
tier floor. `--quiet <tag>` (repeatable, `-Q`) applies the same at
startup and reaches the pre-connect gate notice as well as the in-chat
one; `all` mutes the whole taxonomy. A name outside the taxonomy is
refused, so a switch that no line would ever match cannot be created.

A config written before the tag change keeps working. `ui.notify` decodes
leniently — a value that is not a boolean is dropped rather than
rejecting the file, which would cost the user every other setting — and
the old kind names `pow`, `history`, `join`, `leave` and `date` are read
as the tags they corresponded to. The old `system` name is *not*
translated: it is also a tag, but it meant the catch-all kind and now
means every notice, and widening someone's mute is not a safe guess.

The web settings panel renders the same tree from its own copy of the tag
list, since the page cannot call into Go. A Go test parses that list out
of `web/app.ts` and compares it to `AllTags()`, so the two cannot drift.

## Adding a new tag

1. Add the constant to `internal/wire/tags.go` and, if it has children or
   a parent beyond the root, its position in `tagTree` — parents first,
   siblings stable.
2. Add it to `NOTIFY_TAGS` in `web/app.ts` and re-run `make web-ts`, then
   `make check-web-ts` to confirm the committed `webterm/app.js` matches.
3. Publish it through `noticeWire` or `markerWire`, which take leaves and
   expand the chain. Do not assemble a `Tags` slice by hand.
4. Add the tag to the table above, and to the `produced` map in
   `TestDeclaredTagsAreInTheTree` so removing it from the tree fails a
   test.
5. If it is meant to be a chain-only marker or audit, add a test that the
   tag is reachable there too — `tabForWire` and `handleReplayMarker` are
   the branches that a new marker type has to pass through.

## Compatibility

None. `sys_kind` was replaced, not versioned.

- **A mismatched binary pair is not safe**, in either direction, and fails
  quietly rather than loudly. Upgrade the server and the client together.
  - *New client, old server.* Notices arrive without tags and render as
    generic system lines. Replay **markers** arrive as plain strings, which
    the client no longer recognises, so the replay window state never
    raises: the fork check gets nothing to compare against and is disabled,
    historical echoes get stashed instead of consumed, and the initial load
    pages further than it should.
  - *Old client, new server.* Markers now unmarshal as `type:"system"`
    wires, so the old client renders them and never arms its own window
    tracking — the same three consequences. Notices also lose their
    classification, since the old client reads `sys_kind`.
- **A history file written by an older build** loads as untagged records.
  Its join/leave notices replay to every client regardless of its join
  filter and no longer answer `/notify`. **Deploy against an empty
  history** — see [INSTALL.md](../INSTALL.md).

## Edge cases

- **An empty `sys_date`** means the day is unknown, so the banner always
  prints and nothing is recorded. A duplicate is then possible rather
  than the client guessing a day from the banner's text.
- **A line with several tags** is hidden if *any* of them, or any
  ancestor, is muted. A marker that both names its window and closes it
  (`system.history.recover` + `system.history.end`) is hidden by muting
  either.
- **A muted marker** is not drawn but still tracked: the replay state
  machine, the fork check and the recovery refill all keep working, so
  muting a marker changes what you see, not what the client believes.
- **Markers during a client-driven load** stay hidden regardless of the
  gates, because a page header and footer per page would pepper the
  stream; one banner announces the load. Recovery markers are hidden the
  same way, since the merge prints its own summary.
- **A peer publishing a chain missing a level** is still covered by the
  ancestors it does sit under — see
  [Hierarchy semantics](#hierarchy-semantics).
- **A tag the client does not know** is carried verbatim and simply never
  matches a gate, so a newer peer's line still routes by type and reaches
  Tab 2.

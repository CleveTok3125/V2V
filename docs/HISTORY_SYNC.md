# History Sync Protocol

Status: design spec. Describes the target protocol for fetching stored
history. It is the single mechanism behind the initial client load,
on-demand paging (`/older`), dropped-line refills, and future relay
mirroring. Not implemented yet; this document is the contract to build
against.

## Goals

- One request/response protocol for every history fetch, whatever the
  reason: load a fresh client, page further back, refill dropped lines,
  or mirror a whole history into a relay.
- Client-driven: the server never pushes a history burst on connect.
  The peer asks for exactly the slice it wants, so a slow peer cannot
  overflow the per-session queue and the server never has to drop
  history on the way out.
- Bounded and flow-controlled: each request carries a line limit; the
  peer only asks for the next batch after consuming the previous one,
  so in-flight data stays within one batch.
- Resumable and idempotent: a stable cursor lets a peer stop and resume
  (across reconnects or restarts) without gaps or duplicates.
- Integrity-preserving: chained lines keep their `chain_height` and
  `chain_hash`, so a peer verifies the same chain as live traffic.

## Terminology

- **Stored line** — one persisted history record. It is either a chat /
  audit wire (chained) or a system notice (date, join, leave; unchained).
- **Chained line** — a line that occupies a chain position; it carries
  `chain_height` and `chain_hash`.
- **Unchained line** — a notice with no chain position (`chain_height`
  absent). Dates, joins and leaves are unchained.
- **Seq** — a monotonic sequence number assigned to every stored line,
  chained or not (see below). It is the protocol cursor.
- **Height** — the chain position of a chained line. Stable but absent
  on unchained lines, which is why it cannot order the full stream.

## Sequence numbers

Every stored line gets a `seq`: a server-assigned `uint64` that
increases by one per stored line, in storage order, covering chained
and unchained lines alike. It is carried on the wire as an extra field
on the history wire (`seq`, `omitempty`).

- `seq` is **not** part of the chain hash. The chain hash covers the
  authorship/ordering fields only; adding `seq` cannot change any
  existing link or invalidate stored records.
- `seq` is **not** a security boundary. A peer must not trust `seq` for
  integrity; it is an ordering cursor only. Chain continuity is checked
  via `chain_prev` / `chain_hash` exactly as for live traffic.
- The server assigns `seq` at the single append choke point, before the
  line is broadcast and stored, so the same value reaches live
  subscribers and the persisted record.
- On boot the next `seq` is one past the largest stored `seq`. Records
  written before the field existed are backfilled in load order, so a
  given stored line keeps a stable `seq` across restarts as long as
  storage order is stable (append-only generations).
- Filtered lines never enter storage, so `seq` is gap-free over stored
  lines. Heights are also gap-free over chained lines. Neither is
  guaranteed to be contiguous across an eviction boundary, which is why
  paging is "next cursor", never "cursor + count".

## Cursor model

The cursor is `seq`.

- `after_seq` — ascending: return stored lines with `seq > after_seq`,
  oldest first. `0` means from the oldest line still available.
- `before_seq` — descending: return stored lines with `seq < before_seq`,
  newest first. `0` means from the newest line (the tip).
- The response reports `next_seq`, the cursor to pass to continue in the
  same direction, and `more`, whether any line remains beyond it. A peer
  loops until `more` is false.

Because `seq` orders chained and unchained lines together, a peer
reconstructs the exact stored stream, notices included, without a
separate "attach nearby notices" rule.

## Frames

All frames are JSON. `type` selects the frame; unknown fields are
ignored by older peers.

### `history_info` (server → peer, once after connect)

Announces the available window so a peer can choose where to start
without guessing.

```json
{"type":"history_info","min_seq":1000,"max_seq":1420,"min_height":900,"max_height":1234,"count":421}
```

- `min_seq` / `max_seq` — oldest and newest stored `seq` currently
  served (RAM plus any disk tiers the server exposes).
- `min_height` / `max_height` — chain bounds of the same window, for
  peers that track the chain rather than the full stream.
- `count` — number of stored lines available (may exceed what a single
  request may return; see limits).

### `history_request` (peer → server)

```json
{"type":"history_request","after_seq":0,"before_seq":0,"limit":100,"ranges":[[12124,12196],[12300,12310]]}
```

- `after_seq` — ascending cursor.
- `before_seq` — descending cursor.
- `limit` — maximum lines to return.
- `ranges` — optional list of inclusive `[from, to]` **height** ranges.
  Present only for refill of known-missing chained lines.

Precedence when more than one is set: `ranges` > `after_seq` >
`before_seq`. A request with none of them is invalid.

### `history_sync` (server → peer, trailer)

Closes every history response, after the lines.

```json
{"type":"history_sync","direction":"after","first_seq":1001,"last_seq":1100,"next_seq":1101,"more":true,"sent":100,"total":100,"dropped":0}
```

- `direction` — `"after"` or `"before"`, matching the request.
- `first_seq` / `last_seq` — `seq` bounds of the lines actually sent.
- `next_seq` — cursor to resume with; `0` when there is nothing after.
- `more` — whether more lines exist in that direction.
- `sent` / `total` — lines queued vs lines intended for this response.
- `dropped` — lines the response could not queue (peer buffer full).
  `dropped > 0` marks the response incomplete; the peer should re-request
  the missing span (see Refill) rather than treat it as authoritative.

## Request semantics

- **Ascending (`after_seq`)** returns stored lines with `seq >
  after_seq`, oldest first, including unchained notices.
- **Descending (`before_seq`)** returns stored lines with `seq <
  before_seq`, newest first, including unchained notices. This is what
  `/older` uses.
- **Ranges (`ranges`)** returns only chained lines whose `chain_height`
  falls inside one of the ranges, oldest first. Unchained lines are not
  returned by a range request; a refill only needs the chained gap. The
  peer already has the surrounding notices.
- A response may be capped by the server's per-request line limit (see
  Limits). When capped, `more` is true and `next_seq` points at the
  continuation; the peer issues another request.
- Join/leave notices are filtered from a response when the session did
  not opt into them (`history_joins`), exactly as before. Date and audit
  lines always go. Filtering never changes `seq` or chain positions.

## Paging

Ascending (initial load, relay mirror):

```
cursor = start
loop:
  request {after_seq: cursor, limit: batch}
  receive lines, append in order, verify chain
  cursor = trailer.next_seq
  if !trailer.more: stop
```

Descending (`/older`):

```
cursor = oldest_seq_held
loop (once per user request):
  request {before_seq: cursor, limit: batch}
  receive lines (newest first), reverse, prepend
  cursor = trailer.next_seq
  if !trailer.more: stop
```

A peer resumes by storing `next_seq` and re-requesting with it. Because
`next_seq` is derived from stored lines, a reconnect or restart resumes
without gaps; re-requesting an already-seen span is safe (the peer
dedupes by `seq`).

## Refill

Refill repairs chained lines that were dropped in transit (for example
a live frame lost on a slow link). It uses a `ranges` request so one
round-trip covers every missing run, even when they are scattered.

- The peer tracks the set of missing heights (from a gap it detected, or
  from a `history_sync` trailer reporting `dropped > 0`).
- It coalesces them into contiguous `[from, to]` runs and sends one
  `history_request{ranges: [...], limit: n}`.
- The server returns the chained lines in those ranges, oldest first,
  capped by `limit`; `history_sync` reports what was sent.
- If the response is still incomplete (`dropped > 0`, or some requested
  heights are absent), the peer waits `recoverRetryDelay` and retries the
  still-missing heights, up to `recoverRetries` rounds. Each retry is a
  normal request and therefore obeys the same limits and rate budget.
- A per-session budget (`liveRecoverCap`) caps how many lines a peer
  will auto-refill from live gaps in one session; past it, refill stops
  and the peer reports the loss instead of requesting more. The initial
  load is bounded by its own window and does not draw on this budget.

## Limits and rate budget

- **Per request**: the server clamps `limit` to `MAX_HISTORY_SEND`. A
  response never returns more than that many lines; larger fetches page.
- **Rate**: history requests draw on a per-IP **cost budget** (a token
  bucket), replacing a fixed time cooldown. Each request costs the
  number of lines it may return, scaled by a disk-cost factor when the
  server has to read disk tiers. The budget refills at a configured
  rate up to a configured burst. This lets a legitimate initial load
  (whose total is bounded by the window) proceed immediately while
  sustained abuse drains the budget and is throttled.
- Budget state is keyed by peer address, not by connection, so parallel
  connections from one address share it. Idle entries are evicted like
  other rate maps.
- A request that exceeds the remaining budget is refused with a notice;
  the peer backs off (it may retry later, subject to its own retry
  policy for refills).

## Client initial load

1. Connect and authenticate.
2. Receive `history_info`; pick a start `seq` so the load covers at most
   `initialLines` recent lines (start = `max_seq - initialLines + 1`,
   clamped to `min_seq`).
3. Page ascending in `batchLines` batches until the tip or `initialLines`
   is reached, rendering each batch in order and verifying the chain.
4. Live frames that arrive during the load are held and merged, then the
   whole stream is printed once, so the loaded history and live traffic
   appear as one continuous stream. A held load is released on a timeout
   if the server stops answering.
5. At the end, compare the persisted tip against the received chain (by
   height and hash) to detect a rewrite, exactly as the live path does.

## Relay mirroring

A relay is any peer that keeps a copy of a server's history.

1. Authenticate (the deployment decides what identity a relay needs).
2. Read `history_info` for the available window.
3. Page ascending with `after_seq` from the last `seq` it already holds
   (0 on a cold start), in batches, until `more` is false.
4. Verify each chained line (`chain_prev` / `chain_hash`) and persist in
   order. Store the last `seq` as the resume cursor.
5. Subscribe to the live stream to keep the mirror current; on
   reconnect, resume ascending paging from the stored cursor and dedupe
   by `seq`.

Because paging and live delivery share the same chain and the same
`seq` cursor, a relay needs no separate bulk-export path.

## Edge cases

- **Eviction**: `min_seq` moves forward as the server evicts old lines.
  A cursor below `min_seq` is answered from the oldest available line;
  the trailer's `first_seq` tells the peer what it actually got, and
  `history_info` tells it whether it fell behind.
- **Filtered notices**: they are not stored, so they never consume a
  `seq`; the stream a peer sees is the stored stream.
- **Disk tiers**: when a response spans a disk generation, the disk-cost
  factor raises the request's cost. The protocol is identical; only the
  price changes.
- **Drops**: a response that could not queue every line reports
  `dropped > 0`; the peer re-requests the span instead of trusting it.
- **Legacy records**: records written before `seq` existed are backfilled
  on load, so they page like any other line.

## Configuration

Client (`history`):

- `initialLines` — how much recent history the initial load covers.
- `batchLines` — lines requested per paging round.
- `recoverRetries` — maximum refill retry rounds.
- `recoverRetryDelay` — wait between refill retries.
- `liveRecoverCap` — per-session auto-refill budget for live gaps
  (`0` = unlimited).

Server:

- `MAX_HISTORY_SEND` — per-request line cap (existing).
- `HISTORY_BUDGET_BURST` / `HISTORY_BUDGET_PER_SEC` — cost-budget size
  and refill rate.
- `HISTORY_REFILL_MAX_RANGES` — maximum ranges in one refill request.
- `HISTORY_REPLAY_BATCH_LINES` / `HISTORY_REPLAY_BATCH_BYTES` — how many
  lines the server coalesces into one frame when it answers.

## Compatibility

This replaces the connect-time push replay and the `after` height
cursor; there is no backward-compatibility requirement. A peer that does
not implement `history_info` simply receives no history until it asks.
Legacy stored records are migrated in place by the `seq` backfill.

# V2V Technical Documentation

This document covers the internal architecture, protocols, and algorithms of V2V.
For a friendly getting-started guide, see [README.md](../README.md).

## Table of Contents
- [Project Structure](#project-structure)
- [Build System](#build-system)
- [Client Configuration](#client-configuration)
- [Client Tabs](#client-tabs)
- [Placeholders and Server Echo](#placeholders-and-server-echo)
- [Slash Commands](#slash-commands)
- [Wire Protocol & History](#wire-protocol--history)
- [Message Chain](#message-chain)
- [Authentication](#authentication)
- [Tripcode](#tripcode)
- [Storage & Persistence](#storage--persistence)
- [Security Model](#security-model)
- [Roadmap](#roadmap)

## Project Structure

```
.
├── client/           # CLI and WASM client (shared Go code, platform-specific shims; render.go holds display helpers, client.go the session loop)
├── server/           # WebSocket server, history, auth, WebAuthn
├── internal/
│   ├── identity/     # Shared key file logic (Load/Save, encryption)
│   ├── filter/       # Injection filter (ValidateMessage / SanitizeForDisplay)
│   ├── trip/         # Trip verification (Verify)
│   ├── tripcolor/    # Badge color palette + CanonicalPayload
│   ├── chain/        # Global message hash chain (Hash/VerifyLink/genesis)
│   ├── wire/         # Single protocol source (TripMeta/WireMessage/AuthPacket/HistorySync); client/server alias these types, wire_test pins the JSON key set
│   ├── strength/     # Shared zxcvbn policy (bands, weak gate, bit cap) for client + v2vctl
│   ├── strutil/      # One-line log-truncation helper shared by the two package-main binaries
│   ├── passprompt/   # Masked password entry + strength meter (uses tui line readers and TTY probes)
│   ├── guard/        # Pure send/rate/ban/tripcode policy (fully unit-tested)
│   ├── config/       # Client/server config schema + defaults
│   └── configdir/    # XDG-aware default dirs
│   └── tui/          # General huh confirms/selects + piped fallbacks
│   ├── markup/       # Forum markdown facade over codebg + linkify
│   ├── linkify/      # URL → OSC8 hyperlink
│   └── codebg/       # inline `code` + ``` blocks → background SGR + chroma highlight (display only)
├── webterm/          # Browser terminal (xterm.js + WASM glue)
├── cmd/v2vctl/       # Management tool, one file per concern (main, role, keygen, enroll, migrate, list, prompt)
├── template/         # Example .env / key.json / roles.json
└── docs/             # This file
```

## Build System

All builds are driven by `Makefile`:

```bash
make help            # list targets
make vet test        # GOCACHE=/tmp/gocache go vet/test
make dev             # dev build: bin/v2v, bin/v2v-server, bin/v2vctl + fresh webterm (unstripped, dev-<hash> stamp)
make -j4 all         # parallel: server + web + client + v2vctl (host only for client/v2vctl)
make all ALL=1 -j4   # full 7-platform matrix for client/v2vctl (CI)
make server          # public/server.bin (-tags netgo, -trimpath)
make web             # webterm/app.wasm (+ wasm_exec.js, version.js, gzip/br)
make client          # host only: public/V2V-$(go env GOOS)-$(go env GOARCH)
make client ALL=1    # full matrix: public/V2V-* (7 platforms)
make v2vctl          # host only
make v2vctl ALL=1    # full matrix
make clean
```

- Version stamping: `APP_VERSION=$(git describe --tags --always)` via `-ldflags -X 'main.Version=...'`, also `GIT_HASH` for web.
- Cross-compile: `CGO_ENABLED=0 GOOS=... GOARCH=... go build -trimpath`; host OS detected via `go env GOOS/GOARCH` (`HOST_GOOS/HOST_GOARCH`).
- Default `make client`/`v2vctl` builds only host binary for fast dev; `ALL=1` builds full matrix (7 platforms) for CI.
- CI: `.github/workflows/ci.yml` runs `make vet test` on push to `main/master` and PRs (Go 1.25, cache); `release.yml` runs `make -j4 client v2vctl ALL=1` on tag `v*` and publishes `public/*`.
- Docker: `Dockerfile` runs `make server web` (requires `make` in builder).
- Dev version stamp is always `dev-<HEAD>[-dirty]` from the working tree, never from a possibly stale `GIT_HASH` env; `make web` warns when `GIT_HASH` differs from `HEAD` (stale browser cache risk).

## Client Configuration

- Locations follow the OS (`internal/configdir`): config dir holds `key.json` + read-only `config.jsonc` (`~/.config/V2V/` Linux, `%AppData%\V2V` Windows, `~/Library/Application Support/V2V` macOS); cache dir holds `history.tmp`. Config is JSONC (`//` and `/* */` comments allowed, no trailing commas), never written by the app (missing file means in-memory defaults), and optionally passphrase-sealed with the identity envelope (`v2v --encrypt-config`, `V2V_PASSPHRASE` or TTY prompt to unlock).
- Override with `-c/--config-dir` (`V2V_CONFIG_DIR`) and `-C/--cache-dir` (`V2V_CACHE_DIR`).
- Identity flags: `-k` uses the default key in the config dir, `-K/--key-file <path>` uses an explicit path (old `v2v -k <path>` now errors). No key given means guest mode.
- `template/config.jsonc` documents every group (`defaults`, `network`, `limits`, `guard`, `channels`, `crypto`, `ui`, `commands`, `timeouts`, `tabs`); `internal/config` loads it with `LoadOrCreate`.
- Partial files stay usable: missing `tabs`/`codeStyle` sections, numeric `limits`, and `ui.meta.show` are backfilled.
- Sensitive fields (tripcode, passphrases, private keys) never go in `config.json` — they stay in encrypted `key.json`.
- `ui.meta.show` (default true, `*bool` so absent ≠ false) toggles the trailing `#height:hash` line; `/meta` overrides it for the session only.
- `ui.clipboard.clearAfterSec` (default 30, explicit 0 disables, absent backfills) auto-clears `/copy` output from the OS clipboard, wiping only when it still holds exactly what was copied.
- `ui.mention` (`enabled`, `color [r,g,b]` default bright cyan, `[0,0,0]`/out-of-range falls back) and `ui.reply` (`enabled`, `quoteMaxRunes` clamped 20–200, default 80) tune mentions and reply quotes the same way.
- The verified chain tip persists in `<cache>/chain_tip.json` namespaced by server identity (foreign tips are ignored, never a fork).
- Tab buffer caps come from config, never hardcoded: chat line cap derives from `ui.web.scrollback` (10000), `tabs.chatMaxBytes` (2MiB), `tabs.systemMaxLines` (2000), `tabs.systemMaxBytes` (400KB).

## Client Tabs

Single terminal, two views: Tab 1 (chat + trip badges) and Tab 2 (local, system, date, history). `client/tabs.go:classifyTab` routes each rendered line; `isHistoryBoundaryLine` belongs to TabChat since boundaries delimit chat history.

- Tab 1 shows the full legacy stream even when Tab 2 is active (`emitTab` prints when `tab == activeTab || activeTab == TabChat`), so tabs never change Tab 1 behavior.
- Tab 2 is purely additive and lazyloads (buffered always, rendered on switch).
- Buffers (`tabBuffer`) are FIFO rings with dual-limit eviction (lines + bytes), mirroring server `ChatHistory`; `spliceOut` removes resolved placeholders.
- `/tab`, `/tab 1|2`, `/t` switch with a single-lock clear + replay; the bar shows `[1:chat] 2:system` with the active tab bracketed (`tabBarLine`, columns padded so labels never shift).
- Local command responses (`/help`, `/status`, …) print on the active tab immediately via `emitLocalFeedback` while also buffering into Tab 2 for review.

## Placeholders and Server Echo

- Outgoing text is not echoed optimistically: it prints grey with `⏳` (`| Bạn: … ⏳`), then `BroadcastWire` unicasts the same `WireMessage` back to the sender as delivery confirmation.
- The client tracks pending placeholders (`text/rows/bufEnd/seq/pub`) and on matching echo (same `DisplayName` + `Text`, plus `Pub/Seq` for trip) splices the placeholder out of the tab buffer, erases its screen rows, and reprints any intervening lines.
- The echo then renders through the normal path (badge color + verify link).
- A generation counter (`printGen`) skips the erase when burst output intervened, so wrong rows are never wiped.

## Slash Commands

- Dispatch matches exact tokens (`/help`, `/quit`, …), `/tab`/`/t` with optional `1|2`, `/meta`/`/m` with optional `on|off`, `/find`/`/f` with `<height>[:hash]` and `/info` with `<height>[:hash>`.
- Session commands: `/whoami`/`/w`, `/status`, `/showjoin`/`/sj`, `/autoverify`/`/av`, `/clear`/`/c`, `/clearhistory`/`/ch` (deletes the keystroke history file), `/copy <height>[:hash]` (clipboard, auto-cleared).
- Anything else starting with `/` is an unknown command (`client/commands.go:isUnknownSlashCommand`) rejected locally with `| [Local]: Lệnh không tồn tại…`, never broadcast or trip-signed.
- Code blocks (```) are unaffected, so they double as the escape hatch for sending literal text starting with `/`.
- `/reply <height>[:hash] <text>` quotes a buffered message; the height suffix acts as a typo checksum.
- Bare `/reply <height>` opens a draft: the quote previews at once and the next line becomes the body (any `/` command or empty-line `^C` aborts); only chat messages are quotable, never server markers.
- Quotes resolve per receiver (`wireIdx` first for time/author/verdict, buffer-head fallback) and render `↩ #height | time author ✓/✗: text…` in placeholder and echo alike; the verdict recomputes the target's chain content locally.
- `/info <height>[:hash]` prints the full metadata detail of one indexed wire (height/tmp/reply, full hashes, trip fields with live signature verdict, chain verdict, plus a `raw:` row with stored bytes unrendered: newlines as `⏎`, ESC/control dropped).
- The trip section shows every signature input (pub, seq, prev, sig, msg_hash with text-match mark, server_pub, payload bytes), so the verdict is checkable by hand with any ed25519 tool.
- Wires index by height (cap 1000 FIFO); legacy and evicted report as missing.
- `/find` looks up messages by chain height (the mandatory identifier; a bare hash is rejected since short hashes collide by design, and an appended `:hash` acts only as a typo checksum) across both tab buffers; evicted history reports as not found.
- Code block input (`client.go:collectCodeblock`): a first line that already closes its fence (`codebg.NeedsContinuation`) sends immediately without multiline collection.
- `Ctrl+C` aborts collection via `ErrInputCancel` (empty line cancels on WASM, any interrupt cancels on desktop) and discards everything silently, while `Ctrl+D` (`io.EOF`) keeps quitting.
- `Ctrl+C` on the main prompt prints `| [Local]: Ctrl+C chỉ hủy dòng nhập, thoát app bằng Ctrl+D.` instead of quitting.

## Wire Protocol & History

### Live messages
Chat messages are `WireMessage` JSON, not raw ANSI. The schema lives in `internal/wire` alone; client and server alias it, and `wire_test` pins the exact key set so drift fails loudly instead of dropping fields silently:

```json
{"type":"chat","time":"15:04","displayName":"[Admin] Alice#ab12","text":"hello","tmp_id":7,"reply_to":3,"trip":{"pub":"...","seq":1,"prev":"...","sig":"...","server_pub":"...","msg_hash":"...","display_name":"...","tmp_id":7},"chain_prev":"...","chain_hash":"...","chain_height":1234}
```

- Client → server always travels in a JSON envelope carrying `reply_to` (quote target height, `omitempty`) alongside `tmp_id`; the server relays it verbatim with a cheap existence guard (`reply_to <= tip+1`) and covers it in the v2 link.
- Client → server always travels in a JSON envelope carrying the sender's per-session counter: signed `{text,pub,seq,prev,sig,display_name,tmp_id}` (`client/tripchain.go:TripMessage`) or unsigned `{tmp_id,text}` (`PlainMessage`).
- Raw text is rejected (`server/clientHandler.go:ReadPump`); `tmp_id` must be nonzero.
- `server/clientHandler.go` builds `WireMessage` and broadcasts via `BroadcastWire`. The server relays `tmp_id` verbatim into the wire and history but never assigns or alters it.
- Three broadcast routes, separated by evidence needs. Chat goes through `BroadcastWire`, server management lines through `BroadcastAudit`; both chain via the single choke point `linkAndStore`, so every evidence record carries a chain link. Join/leave/date notifications go through `BroadcastNotice`: tagged `SysKind` (`"join"`/`"leave"`/`"date"`, `"audit"` for audit lines), stored and broadcast, but never chained — they carry no authorship or ordering evidence. Per-client unicast warnings stay raw strings and are never chained.
- `BroadcastAudit` currently has no producers: it is a reserved route so future management actions (ban/kick/mute/rolechange) chain as evidence instead of riding the notice path. When adding one, call `BroadcastAudit` (never `BroadcastNotice`) and assert the line verifies and replays in tests.
- Client `client/client.go` receives `WireMessage` JSON (`chat`/`system`) or legacy raw lines; it tries `json.Unmarshal` and falls back to plain display.
- Records without chain fields render without a meta line.

### History persistence
- File: `data/history.jsonl` — one JSON record per line: `{"ts":"RFC3339Nano","wire":{...}}` for chat, `{"ts","msg":"..."}` for system messages.
- The top-level `trip` field was removed (dedup); only `wire.trip` is kept.
- Rotation: when `size > MAX_HISTORY_FILE_SIZE_MB` (`50MB` in `template/.env`), current file is renamed to `.old` and compressed to `.old.zst` via `klauspost/compress/zstd` (`50MB → ~3MB`).
- At most 2 generations are kept (`~53MB` max). `LoadRecords` tries `.old.zst`, then `.old`, then current.
- Durability: `HistoryStore.writeLoop` batches `Sync` every `1s` **only when dirty** (`dirty` flag set on `Write`, cleared on `Sync`), plus `SIGTERM` drain via `HistoryStore.Close()` in `server/main.go`.
- Directory `fsync` after rotate (like `webauthn_store.go`).

### In-memory history
- `server/shared.go:ChatHistory []string` — deduped `WireMessage` JSON strings, evicted by `MaxHistoryBytes` (`10MB`), with `cap > 4*len` shrink to avoid 20MiB bloat.
- `SendChatHistory` streams `MaxHistorySend` (`500`) messages without extra copy.
- Join/leave lines carry `sys_kind` set at broadcast; catch-up replay filters them unless the session asked (`AuthPacket.history_joins`, wired to the client `-j` flag, which also seeds live join display — `/showjoin` only toggles live display afterwards). Dates, audits and untagged lines always go, including audits when joins are filtered. Live broadcasts always carry every line; only replay filters. The web client exposes the same knob as its show-join checkbox.
- Each replay closes with a counted human footer (`--- Kết thúc lịch sử (sent/total) ---`, opened by `--- Lịch sử chat gần đây ---`) plus a machine `history_sync` trailer for the fork check: `{"type":"history_sync","min_height":H,"max_height":H,"sent":S,"total":T}`. Filtered lines never held chain positions, so the replayed window has no gaps. Notices carry `chain_height == 0` and never widen the trailer window.
- System broadcasts (`join/leave/date`) retry once after `20ms` before dropping (`sendWithRetry`), so a chat burst filling the per-client `Send` queue (256) does not silently swallow system lines; chat itself stays best-effort.

## Message Chain

Every chat and audit message links to the previous one (`internal/chain`, `server/chain.go`) — altering any byte of a linked message breaks the link and every link after it, with no per-user signing required. Join/leave/date notifications never link: they carry no authorship or ordering evidence.

- **Link:** `chain_hash = sha256("V2V-chain-v1" ‖ len-prefixed(prev, height, tmp_id, reply_to, type, time, displayName, text, tripSig))`, versioned by `chain_ver`.
- Version 1 = pre-reply encoding without the replyTo segment, always 2 on new links; old records keep verifying.
- Links compute at the single choke point `linkAndStore` under `BroadcastMu`, so send order always matches chain order and `prev`-continuity checks never false-positive on reorder.
- **Numbering:** `chain_height` is a global ever-increasing counter (displayed as `#height:hash4`) and the mandatory message identifier; `hash4` stays a fixed auxiliary visual aid since short hashes collide by design.
- `tmp_id` is the sender's per-session counter (server-originated system wires carry none). Both are covered by the link hash.
- **Genesis/resume:** the seed derives from the server identity (`chain.Genesis`), so restarts resume the same chain; pre-chain legacy history anchors via `chain.LegacyAnchor` without being rewritten. Unchained system lines are skipped for anchor and verification alike, so notification bursts can never hijack the resume anchor.
- A broken stored link logs `[CHAIN TAMPER]` and adopts the last record (availability); online clients holding older tips flag the fork.
- **Client:** `client/chainmeta.go` + read loop verify content hash on every wire and prev continuity once a tip is adopted; the tip persists in `<cache>/chain_tip.json`. After the trailer, a persisted tip inside the replayed window but absent from the received hashes warns about a fork; tips outside the window adopt silently.
- Own echoes match placeholders by exact `tmp_id` (`matchPendingIndex`); echoes arriving before their placeholder are stashed and retried at track time, expiring with a warning after `10s` (how plain-path ID tampering surfaces).
- `tmp_id` starts at a random 2³² base per connection and history replay never feeds the stash, so reconnects cannot collide with old sessions.
- `trip.Verify` additionally binds `tmp_id` into the trip signature, so renumbering a signed message fails verification on both ends; `tmp_id 0` falls back to the legacy payload encoding so pre-upgrade history and old browser links verify read-only.
- **Limits (honest):** a fully malicious server can rewrite the whole chain from genesis for brand-new clients holding no prior tip — like every transparency log without witnesses. Online clients and persisted tips detect the fork.
- Unsigned plain messages have no authorship proof; the chain gives them ordering + tamper evidence, not attribution.

## Authentication

### Ed25519 (key file)
- Key file `key.json` (`identity/identity.go`) is a versioned container (`version:3`) with one `ed25519` slot and one `passkey` slot.
- `Ed25519Identity` stores `role`, `private_key` (hex 128), `hmac_shield` (hex 32), `server_pubkey` (hex 64, from `data/server_identity.json`).
- Handshake: server sends `auth_challenge {nonce, serverPubkey, serverSig}` where `serverSig = ed25519.Sign(serverPriv, "V2V-SERVER-v1\x00"+nonce+"\x00"+host)`.
- Client verifies `serverSig` against `serverPubkey` pin (or `server_pubkey` in `key.json`), warns on mismatch, then signs `dataToSign = nonce|role|username|serverPub` with its private key.
- Client sends `signature` + `hmac = HMAC-SHA512(signature + nonce, hmac_shield)`. Server verifies `ed25519` and `hmac.Equal`, checks `ServerPubKey` pin, and enforces `TripChains` for trip users.
- `HMAC` with `bytes(signature)` prevents replay without the shield even if private key is exposed.

### Passkey (WebAuthn)
- `PasskeyIdentity` stores `credential_id`, `private_key` (PKCS8), `public_key` (COSE CBOR), `rpid`, `origin`, `signCount`.
- Web enrollment: `v2vctl enroll --role member` creates a one-time ticket (`/webauthn/enroll/begin` → `navigator.credentials.create` → `/webauthn/enroll/finish`), stored in `data/webauthn.json` (`WebAuthnStore`).
- Login verifies `authenticatorData`, `clientDataJSON`, `rpIdHash`, `origin`, and `counter` (clone detection).

### Display name — uniform hash, serial, dynamic length, per-session salt
`server/auth.go:generateDisplayName` validates `username` via `filter.ValidateDisplayName`, trims and caps to `MaxUsernameLength`, then **always** appends a hash suffix — even for roles with `CustomPrefix` (`roles.json`). No role is exempt:

- **Salt:** `server/shared.go:ChatServer.DisplaySalt` — 32B `crypto/rand` generated once per server run in `NewChatServer` (ephemeral, never persisted or logged, distinct from `server_identity.json`'s long-term Ed25519 key).
- The hash is `HMAC-SHA256(salt, IP)` (`auth.go:366`), not plain `SHA256(IP)`, so knowing the hash does not reveal the IP and a restart rotates all hashes.
- **Hash length (dynamic):** `hashLen` is computed from live connections `len(Clients)` (`auth.go:360`): `4` chars (16-bit) by default, `5` when `n>100`, `6` when `n>800`, clamped `4..6`.
- Short names stay short with few users online; collision probability stays low as concurrency grows.
- **Serial for duplicates:** `server/shared.go:DisplayNameCount map[string]int` + `DisplayNameCountMu` tracks active `fullDisplayName`s.
- If `base = prefix+name+"#"+hash` already exists, the next duplicate becomes `base-2`, then `-3`, etc. (`auth.go:370`).
- On `unregisterClient` the exact `session.DisplayName` is `delete`d (`clientHandler.go:82`), freeing the slot. The check and claim are `O(1)` and happen once per login.

### Connection serving order
- `serveAuthenticated` starts `WritePump` before `registerClient`: history replay pushes up to `MaxHistorySend` lines into the buffered `Send` channel synchronously, so registering first with a full history and no reader deadlocks every new connection.
- History sends are non-blocking with drop (`sendWithRetry`); unicast guard warnings never block `ReadPump` on a wedged pump.
- The history disk queue is non-blocking with a drop counter and idempotent `Close`, so a stalled disk sheds load instead of stalling broadcasts.
- Pre-auth nonces expire by sweep (30s ticker), not one timer per connection, and expiry is enforced again at consume.

Final form: `[CustomPrefix]name#hash` or `[CustomPrefix]name#hash-2` (e.g., `[Admin] Alice#a1b2`, `Bob#a1b2-2`). The full `displayName` (including hash and serial) is what is signed in trip messages (`payload = serverPub|seq|prev|msgHash|pub|displayName|tmpID`) and stored in `WireMessage`/`TripMeta`.

A spoofed `displayName` fails trip verification.

Performance: one `HMAC` per login (`µs`), `hashLen` calc is `O(1)` with `RLock`, serial map is `O(1)`. No per-message cost.

## Tripcode

Tripcode is a per-user pseudonym independent from roles, derived from a passphrase.

### Secret resolution
- `-t` takes no value. Resolution order: `V2V_TRIPCODE` env (CI only) > `tripcode.json` in the config dir > hidden interactive prompt.
- TTY prompts: `internal/passprompt` for masked password entry, `internal/tui` for huh confirms and selects defaulting to No; piped stdin keeps the plain-line fallback.

### Interactive entry
- Manual entry shows a static reminder (long memorable sentence, ≥20 runes, ≥4 parts split on any non-letter/digit — advisory only).
- TTY reads the secret once, then verifies it with an immediate re-entry round before anything is asked about saving.
- Piped stdin keeps upfront double-entry (exactly 2 lines per round, max 3 rounds).
- First manual entry offers an encrypted save (`y/N`, default No).
- Pre-loop prompts share one unbuffered stdin reader so piped answers never starve later readers (prompts, then readline's chat loop).

### Strength meter and gates
- Live meter above the field: `<bar> <bits> bits — <label>`. Bar, color, and label all follow score 0-4; bits stay informational.
- Bits cap at 128 with a `+` suffix when clamped. Labels: 0-1 yếu / 2 trung bình / 3 mạnh / 4 rất mạnh.
- score≤1 warns and requires explicit `Vẫn dùng?` confirm (default No).
- A weak passphrase warns and confirms right after the first entry, before any re-entry round; piped input warns only.
- `v2vctl` keygen/migrate passphrases carry the same gate; unlock prompts never gate.
- Strength comes from zxcvbn (`ccojocar/zxcvbn-go`, pure Go, +0.52MB raw/+0.39MB gzip wasm) with username/hostname as personal context.
- Its crack-time display is never shown (fast-hash assumption understates argon2id cost ~1e9).
- Gate rationale: native derive costs ~30ms/guess (measured), so score≤1 (<1e6 guesses) falls in days of single CPU — opportunistic-attacker territory.

### File format and memory
- The file holds `{"version":3,"tripcode":"..."}` in the same encrypted v3 envelope (argon2id + XChaCha20Poly1305) as `key.json`, unlocked via `V2V_PASSPHRASE` env or hidden prompt.
- Plaintext files are refused fail-closed and nothing is ever saved unencrypted (empty unlock skips saving).
- File loads evaluate silently and print only a warn line when weak (no entropy); env path log-warns and proceeds.
- Env secrets never touch UI: every env path only log-warns when weak and proceeds, whether it unlocks, saves, or feeds a session tripcode.
- Passphrases cross the identity boundary as `[]byte` and are wiped after use (argon2 keys always, caller buffers via `ZeroBytes`, xterm read buffer on return).
- Prompt/URL/TTY strings and the session unlock cache await GC and are documented as residual.

### Derivation
- `salt = sha256(serverPub)[:16]` (or `sha256("V2V-trip-v1")[:16]` if no serverPub), `seed = argon2id(passphrase, salt, t=1/m=32MB WASM or t=3/m=64MB native)`, `ed25519.NewKeyFromSeed(seed)`, `badge = "◆ " + hex(sha256(pub))[:8]`.
- The passphrase is zeroed after derive; private key is kept in RAM until `/quit` then zeroed.
- Colors come from a fixed 10-color `38;2` palette rotated by `sha256(badge)[0]`.

### Per-message
- `msgHash = sha256(text)`, `payload = serverPub\x00seq\x00prev\x00msgHash\x00pub\x00displayName\x00tmpID\x00replyTo` (`internal/tripcolor.CanonicalPayload`), `sig = ed25519.Sign(priv, payload)`, `prev' = sha256(prev|sig|msgHash)`, `seq` strictly increasing per `pub` (enforced via `TripChains` per-pub mutex `TripChainsMu`).

### Transport
- `client/tripchain.go:TripMessage` JSON `{text,pub,seq,prev,sig,displayName,tmp_id}` via `WriteJSON`; server `ReadPump` parses it, validates `filter.ValidateMessage(text)`, checks `pub == session.TripPub`, checks `seq == last+1` and `prev == lastPrev`, verifies `msgHash` and `ed25519`, updates chain, stores `WireMessage{Text, TripMeta}`.

### Verification
- Both server (`ReadPump`, `history.go:InitHistoryStore`, `trip_api.go`) and client (`verifyCh` FIFO queue, parallel workers, `autoVerify` default on, `/autoverify` toggle) recompute `msgHash` and `Verify`, then recolor badge (`palette` if valid, `91m` red `✗` if tampered).
- History file edits without updating `sig` are detected as `HISTORY TAMPER`.

### Link
- Badge is wrapped in OSC8 `https://<host>/api/trip/verify?pub&seq&prev&sig&msg_hash&server_pub&display_name&tmp_id&reply_to` (stateless `GET /api/trip/verify`, rate-limited `200ms/IP`, capped `2048` query).
- No `text=` — `msg_hash` suffices, keeping links bounded and content out of URLs, while old links carrying text keep working.
- `serverPub` is enforced to be the server's own key to prevent cross-server reuse.
- `linkify` (server) and `webterm/app.js` (browser) handle `https` links; `v2v://` legacy is removed.
- Browser navigation (`Accept: text/html`) gets the self-contained `webterm/verify.html` page instead of JSON: verdict pill, a paste-to-verify content section (SHA-256 recomputed locally against `msg_hash`), per-field rows with copy buttons, an API-link section, and collapsible raw JSON.
- The page re-fetches the same URL with `Accept: application/json`, so curl/fetch behavior is unchanged.
- `/copy <height>[:hash]` copies raw message text to the OS clipboard for pasting into the page (plaintext lives in the clipboard until auto-cleared or overwritten — any local app can read it).
- Verify pages require HTTPS/localhost, and plain-HTTP shows a warning banner since pasted text could be intercepted in transit.
- `display_name` stays in badge URLs because the signature binds it, and it is only a pseudonym.

## Message Rendering

- `markup.Span` renders forum markdown client-side: `**bold**` (`1m`), `*italic*` (`3m`), `~~strike~~` (`9m`), `[text](url)` (linkify-styled OSC 8 hyperlink, http(s) only), and `> quote` (grey `│ ` bar), parsed by goldmark with a custom SGR renderer.
- Code spans and fenced blocks delegate to `codebg`, so code styling has a single source; headings, lists, HTML and images degrade to raw source text, never lost.
- Only zero-width escapes are added, so erase math holds on both render paths (`Span` for incoming, `SpanPlain` for the placeholder, mirroring the `RenderWithStyle`/`Render` split).
- Bare URLs stay linkify's job; text with ESC passes through untouched.
- Mentions `@#height[:hash]` highlight (accent) when the target resolves in the local buffer (suffix checksum, SGR/OSC8 spans skipped, evicted stays plain); `#height` without `@` is plain text.
- Messages bypass the render cache when they carry mentions or quotes, whose resolution depends on buffer state.
- Replies (`/reply <height>[:hash] <text>`) render a `| ↩ #height: <first line…>` quote above content in placeholder and echo alike (same builder, so erase math holds); quotes always resolve locally, so misattributed quotes expose the liar.
- Evicted targets reject the reply before send.
- Every chained chat message renders as content rows plus exactly one trailing meta line: `|   └─  #height:hash4`, with ` | ✍️ ◆ badge` (hyperlink kept) appended for trip messages.
- Server notices (date, join/leave) are never chained and never verified, and render no meta line. Audit lines chain like chat (evidence) but render as plain system rows.
- The sender placeholder ends with `|   └─  ··· ⏳` until the echo (carrying the real position) replaces the whole block, so erase counts stay exact.
- One `renderChatBlock` serves live, echo, history and placeholder paths; legacy lines without chain fields render content only, and pure-local lines (`[Local]`, date banners drawn client-side) carry no meta.
- `codebg.Render` wraps inline `` `code` `` spans and ``` fenced blocks in a background SGR (`48;5;236`, closed with `49m` so ambient foreground survives), stripping the backtick delimiters markdown-style.
- A ```lang opener becomes a header line showing just the language name (no language means no header), the closing fence is dropped, and single-line ```code``` renders as one background line.
- Only zero-width escapes are added or removed, so trip `msg_hash` (over raw text) is unaffected; placeholder erase counts are recomputed from the rendered split.
- Unmatched backticks, empty spans and text containing ESC pass through unchanged.
- `codebg.RenderWithStyle` adds chroma syntax highlighting for fenced blocks with a known language tag (custom per-line SGR emitter, truecolor `38;2` foreground on the configured background, every line self-closed so tab buffers stay consistent).
- Inline spans, untagged blocks, unknown languages and oversize blocks (>64KiB) fall back to the plain background.
- Palette comes from `ui.codeStyle` in `config.json` (`background`, `keyword`, `string`, `comment`, `number`, `name`, `function`, `type`, `operator` as `[r,g,b]`; a `[0,0,0]` entry falls back to the compiled default), backfilled by `internal/config` like `tripPalette`.
- Applied client-side after `SanitizeForDisplay` on incoming chat text; the sender placeholder echo keeps plain `Render` so highlight resets don't cancel its grey wrapper (line counts match either way).
- Applied client-side after `SanitizeForDisplay` on incoming chat text and after `Linkify` on the sender placeholder echo (whole text rendered before splitting, so fence state survives across lines).

## Storage & Persistence

- `data/server_identity.json` — server's long-term Ed25519 keypair, auto-generated, used for `serverPub` pinning.
- `data/history.jsonl` / `.old.zst` — chat history, `zstd` compressed old generation, smart batch `Sync`.
- `data/webauthn.json` — WebAuthn tickets and credentials, `atomicWriteFile` via `CreateTemp+Sync+Rename+dir Sync`.
- `key.json` — encrypted at rest via `XChaCha20Poly1305 + Argon2id` (`version:3` envelope, `chmod 600`, `V2V_PASSPHRASE` env or hidden prompt via `internal/passprompt` on TTY / `charmbracelet/x/term` piped fallback, same flow as `v2vctl`).
- The successful unlock secret is remembered for the session so counter saves re-encrypt instead of dropping to plaintext, and wiped at exit (`ClearLoadedPassphrase`, deferred plus the conn-drop path).
- File-supplied argon2 costs are clamped (t 1-10, m 8-256MiB, p 1-8) and the envelope identity (v3/argon2id/xchacha20poly1305) verified, so crafted files fail closed instead of exhausting RAM.

## Security Model

- **Injection:** All inbound `text` and `username` go through `internal/filter`.
- `ValidateMessage` rejects `Cf/Mn/Me/Zl/Zp/0xFFFD/non-graphic`, `SanitizeForDisplay` keeps only whitelisted `SGR \x1b[...m` and `OSC8 \x1b]8;;...\x1b\\`; an unterminated OSC8 drops the tail instead of leaking the link target.
- Client double-filters before display, so a compromised server's tampered history cannot execute `ESC[2J` etc.
- **Phishing:** Privileged identities are pinned to `server_pubkey` (not hostname); real passkeys are pinned by `RPID`/`origin`.
- **Spam/Abuse:** `MaxConnectionsPerIP`, `MessageCooldown`, `IdleChatTimeout`, `Trip verify 200ms/IP` rate limit, `SetReadLimit` `64KB` for auth and `MaxMessageLength*3` for chat.
- **Transport:** `REQUIRE_TLS` option blocks `ws://` (returns `426`), `ALLOWED_ORIGINS` checked in `Upgrader.CheckOrigin`.
- `IsSecuredConnect` trusts `X-Forwarded-Proto` only behind proxy.
- **Outbound proxy (desktop client):** `--proxy URL` / `V2V_PROXY` env beat the system `HTTP(S)_PROXY`/`NO_PROXY` (gorilla `DefaultDialer` still honors those when nothing is set).
- `--ask-proxy` runs an interactive wizard (huh scheme select, host, port, optional user, hidden password) that overrides all static config.
- HTTP(S) proxies use a dedicated gorilla `Dialer`; SOCKS5 handshakes by hand on stdlib (`client/proxy.go`, no new dependency) with the target always sent as a domain name so no local DNS leaks, plus TLS for `wss`.
- The proxy password is the proxy's secret: no meter, no weak gate. It lives as `[]byte`, wipes after dial, and logs show `user:***@host`; prompt/URL strings at the stdlib boundary await GC as documented for all secrets.

## Roadmap

Planned future work, in no particular order. Each item is self-contained: it can land without the others.

- **Paged history** — fetch older segments on demand (`/history`); the connect-time replay (`MAX_HISTORY_SEND`) stays a join burst for fast startup.
- **Session surgery** — extract main-loop session state (`term/out/displayMu/tabs/chain/verify`) and split `ChatServer` fields (`chain.Service`/`history.Store`/`hub`); the client tip-state mutex depends on this refactor.
- **First audit producer** — the `BroadcastAudit` route has tests but no callers; wire the first management action (ban/kick/mute/rolechange) through it, with verify and replay tests proving the line chains and replays.
- **Env/log/error unification** — one typed getenv helper replacing scattered `os.Getenv`, leveled logging replacing bare `log.Printf`, and a documented rule for when errors wrap with `%w`.
- **Dependency arrows** — use the `markup` facade instead of importing `codebg`/`linkify` directly; `strength` returns its own report type instead of `passprompt.Assessment`; `guard` takes a plain limits struct instead of `*config.DynamicConfig`.
- **Wasm and timing tests** — coverage for the terminal emulator and proxy paths on wasm; bounded waits instead of fixed sleeps in timing-sensitive tests.
- **Client config encryption** — guard limits (`MaxMessageLength`, cooldowns) currently load from plaintext `config.json`, so editing the local file weakens client-side guards. Encrypt the client config with the tripcode v3-envelope pattern (argon2id + XChaCha20, passphrase-opened), reusing the existing prompt and unlock flows; no new secrets, server `.env` out of scope.
- **Relay mesh** — a lightweight distribution network outside the server: each relay connects to one server plus many relays, and each client connects to one server plus many relays (CDN-style reads). Writes go to the server only (relays are read-only); relays share one wire protocol subset, alert each other with the client as the consumer, and serve as backup sources the client verifies against known chain tips. Not a federation: no cross-server identity or routing, one server's content only.

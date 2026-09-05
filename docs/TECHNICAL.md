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

## Project Structure

```
.
├── client/           # CLI and WASM client (shared Go code, platform-specific shims)
├── server/           # WebSocket server, history, auth, WebAuthn
├── identity/         # Shared key file logic (Load/Save, encryption)
├── internal/
│   ├── filter/       # Injection filter (ValidateMessage / SanitizeForDisplay)
│   ├── trip/         # Trip verification (Verify)
│   ├── tripcolor/    # Badge color palette + CanonicalPayload
│   ├── chain/        # Global message hash chain (Hash/VerifyLink/genesis)
│   └── wire/         # Shared TripMeta / WireMessage / AuthPacket types (future)
├── linkify/          # URL → OSC8 hyperlink
├── codebg/           # inline `code` + ``` blocks → background SGR + chroma highlight (display only)
├── webterm/          # Browser terminal (xterm.js + WASM glue)
├── cmd/v2vctl/       # Management tool (keygen, enroll, list, migrate)
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

- Locations follow the OS (`internal/configdir`): config dir holds `key.json` + auto-created `config.json` (`~/.config/V2V/` Linux, `%AppData%\V2V` Windows, `~/Library/Application Support/V2V` macOS); cache dir holds `history.tmp`. Override with `-c/--config-dir` (`V2V_CONFIG_DIR`) and `-C/--cache-dir` (`V2V_CACHE_DIR`).
- Identity flags: `-k` uses the default key in the config dir, `-K/--key-file <path>` uses an explicit path (old `v2v -k <path>` now errors). No key given means guest mode.
- `template/config.json` documents every group (`defaults`, `network`, `limits`, `guard`, `channels`, `crypto`, `ui`, `commands`, `timeouts`, `tabs`); `internal/config` loads it with `LoadOrCreate` (missing `tabs` section is backfilled). Sensitive fields (tripcode, passphrases, private keys) never go in `config.json` — they stay in encrypted `key.json`.
- Tab buffer caps come from config, never hardcoded: chat line cap derives from `ui.web.scrollback` (10000), `tabs.chatMaxBytes` (2MiB), `tabs.systemMaxLines` (2000), `tabs.systemMaxBytes` (400KB).

## Client Tabs

- Single terminal, two views: Tab 1 (chat + trip badges) and Tab 2 (local, system, date, history). `client/tabs.go:classifyTab` routes each rendered line; `isHistoryBoundaryLine` belongs to TabChat since boundaries delimit chat history.
- Tab 1 shows the full legacy stream even when Tab 2 is active (`emitTab` prints when `tab == activeTab || activeTab == TabChat`), so tabs never change Tab 1 behavior; Tab 2 is purely additive and lazyloads (buffered always, rendered on switch).
- Buffers (`tabBuffer`) are FIFO rings with dual-limit eviction (lines + bytes), mirroring server `ChatHistory`; `spliceOut` removes resolved placeholders. `/tab`, `/tab 1|2`, `/t` switch with a single-lock clear + replay; the bar shows `[1:chat] 2:system` with the active tab bracketed (`tabBarLine`, columns padded so labels never shift).
- Local command responses (`/help`, `/status`, …) print on the active tab immediately via `emitLocalFeedback` while also buffering into Tab 2 for review.

## Placeholders and Server Echo

- Outgoing text is not echoed optimistically: it prints grey with `⏳` (`| Bạn: … ⏳`), then `BroadcastWire` unicasts the same `WireMessage` back to the sender as delivery confirmation.
- The client tracks pending placeholders (`text/rows/bufEnd/seq/pub`) and on matching echo (same `DisplayName` + `Text`, plus `Pub/Seq` for trip) splices the placeholder out of the tab buffer, erases its screen rows, reprints any intervening lines, and renders the echo through the normal path (badge color + verify link). A generation counter (`printGen`) skips the erase when burst output intervened, so wrong rows are never wiped.

## Slash Commands

- Dispatch matches exact tokens (`/help`, `/quit`, …), `/tab`/`/t` with optional `1|2`, and the `/verify` prefix. Anything else starting with `/` is an unknown command (`client/commands.go:isUnknownSlashCommand`) rejected locally with `| [Local]: Lệnh không tồn tại…`, never broadcast or trip-signed. Code blocks (```) are unaffected, so they double as the escape hatch for sending literal text starting with `/`.
- Code block input (`client.go:collectCodeblock`): a first line that already closes its fence (`codebg.NeedsContinuation`) sends immediately without multiline collection; `Ctrl+C` aborts collection via `ErrInputCancel` (empty line cancels on WASM, any interrupt cancels on desktop) and discards everything silently, while `Ctrl+D` (`io.EOF`) keeps quitting. `Ctrl+C` on the main prompt prints `| [Local]: Ctrl+C chỉ hủy dòng nhập, thoát app bằng Ctrl+D.` instead of quitting.

## Wire Protocol & History

### Live messages
Chat messages are `WireMessage` JSON, not raw ANSI:

```json
{"type":"chat","time":"15:04","displayName":"[Admin] Alice#ab12","text":"hello","tmp_id":7,"trip":{"pub":"...","seq":1,"prev":"...","sig":"...","server_pub":"...","msg_hash":"...","display_name":"...","tmp_id":7},"chain_prev":"...","chain_hash":"...","chain_height":1234}
```

- Client → server always travels in a JSON envelope carrying the sender's per-session counter: signed `{text,pub,seq,prev,sig,display_name,tmp_id}` (`client/tripchain.go:TripMessage`) or unsigned `{tmp_id,text}` (`PlainMessage`). Raw text is rejected (`server/clientHandler.go:ReadPump`); `tmp_id` must be nonzero.
- `server/clientHandler.go` builds `WireMessage` and broadcasts via `BroadcastWire`. The server relays `tmp_id` verbatim into the wire and history but never assigns or alters it.
- System lines (join/leave/date) are `WireMessage{Type:"system"}` through `BroadcastSystem`, so every broadcast message carries a chain link. Per-client unicast warnings stay raw strings and are never chained.
- Client `client/client.go` receives `WireMessage` JSON (`chat`/`system`) or legacy raw lines; it tries `json.Unmarshal` and falls back to plain display. Records without chain fields render without a meta line.

### History persistence
- File: `data/history.jsonl` — one JSON record per line: `{"ts":"RFC3339Nano","wire":{...}}` for chat, `{"ts","msg":"..."}` for system messages. The top-level `trip` field was removed (dedup); only `wire.trip` is kept.
- Rotation: when `size > MAX_HISTORY_FILE_SIZE_MB` (`50MB` in `template/.env`), current file is renamed to `.old` and compressed to `.old.zst` via `klauspost/compress/zstd` (`50MB → ~3MB`). At most 2 generations are kept (`~53MB` max). `LoadRecords` tries `.old.zst`, then `.old`, then current.
- Durability: `HistoryStore.writeLoop` batches `Sync` every `1s` **only when dirty** (`dirty` flag set on `Write`, cleared on `Sync`), plus `SIGTERM` drain via `HistoryStore.Close()` in `server/main.go`. Directory `fsync` after rotate (like `webauthn_store.go`).

### In-memory history
- `server/shared.go:ChatHistory []string` — deduped `WireMessage` JSON strings, evicted by `MaxHistoryBytes` (`10MB`), with `cap > 4*len` shrink to avoid 20MiB bloat. `SendChatHistory` streams `MaxHistorySend` (`500`) messages without extra copy.
- System broadcasts (`join/leave/date`) retry once after `20ms` before dropping (`sendWithRetry`), so a chat burst filling the per-client `Send` queue (256) does not silently swallow system lines; chat itself stays best-effort.

## Message Chain

Every broadcast message links to the previous one (`internal/chain`, `server/chain.go`) — altering any byte of any message breaks the link and every link after it, with no per-user signing required:

- **Link:** `chain_hash = sha256("V2V-chain-v1" ‖ len-prefixed(prev, height, tmp_id, type, time, displayName, text, tripSig))`, computed at the single choke point `linkAndStore` under `BroadcastMu`, so send order always matches chain order and `prev`-continuity checks never false-positive on reorder.
- **Numbering:** `chain_height` is a global ever-increasing counter (displayed as `#height:hash4`); `tmp_id` is the sender's per-session counter (server-originated system wires carry none). Both are covered by the link hash.
- **Genesis/resume:** the seed derives from the server identity (`chain.Genesis`), so restarts resume the same chain; pre-chain legacy history anchors via `chain.LegacyAnchor` without being rewritten. A broken stored link logs `[CHAIN TAMPER]` and adopts the last record (availability); online clients holding older tips flag the fork.
- **Client:** `client/chainmeta.go` + read loop verify content hash on every wire and prev continuity once a tip is adopted; the tip persists in `<cache>/chain_tip.json`, and after history sync a persisted tip missing from the replay warns about a fork. Own echoes match placeholders by exact `tmp_id` (`matchPendingIndex`); echoes arriving before their placeholder are stashed and retried at track time, expiring with a warning after `10s` (how plain-path ID tampering surfaces). `trip.Verify` additionally binds `tmp_id` into the trip signature, so renumbering a signed message fails verification on both ends; `tmp_id 0` falls back to the legacy payload encoding so pre-upgrade history and old browser links verify read-only.
- **Limits (honest):** a fully malicious server can rewrite the whole chain from genesis for brand-new clients holding no prior tip — like every transparency log without witnesses. Online clients and persisted tips detect the fork. Unsigned plain messages have no authorship proof; the chain gives them ordering + tamper evidence, not attribution.

## Authentication

### Ed25519 (key file)
- Key file `key.json` (`identity/identity.go`) is a versioned container (`version:3`) with one `ed25519` slot and one `passkey` slot. `Ed25519Identity` stores `role`, `private_key` (hex 128), `hmac_shield` (hex 32), `server_pubkey` (hex 64, from `data/server_identity.json`).
- Handshake: server sends `auth_challenge {nonce, serverPubkey, serverSig}` where `serverSig = ed25519.Sign(serverPriv, "V2V-SERVER-v1\x00"+nonce+"\x00"+host)`. Client verifies `serverSig` against `serverPubkey` pin (or `server_pubkey` in `key.json`), warns on mismatch, then signs `dataToSign = nonce|role|username|serverPub` with its private key and sends `signature` + `hmac = HMAC-SHA512(signature + nonce, hmac_shield)`. Server verifies `ed25519` and `hmac.Equal`, checks `ServerPubKey` pin, and enforces `TripChains` for trip users.
- `HMAC` with `bytes(signature)` prevents replay without the shield even if private key is exposed.

### Passkey (WebAuthn)
- `PasskeyIdentity` stores `credential_id`, `private_key` (PKCS8), `public_key` (COSE CBOR), `rpid`, `origin`, `signCount`.
- Web enrollment: `v2vctl enroll --role member` creates a one-time ticket (`/webauthn/enroll/begin` → `navigator.credentials.create` → `/webauthn/enroll/finish`), stored in `data/webauthn.json` (`WebAuthnStore`). Login verifies `authenticatorData`, `clientDataJSON`, `rpIdHash`, `origin`, and `counter` (clone detection).

### Display name — uniform hash, serial, dynamic length, per-session salt
`server/auth.go:generateDisplayName` validates `username` via `filter.ValidateDisplayName`, trims and caps to `MaxUsernameLength`, then **always** appends a hash suffix — even for roles with `CustomPrefix` (`roles.json`). No role is exempt:

- **Salt:** `server/shared.go:ChatServer.DisplaySalt` — 32B `crypto/rand` generated once per server run in `NewChatServer` (ephemeral, never persisted or logged, distinct from `server_identity.json`'s long-term Ed25519 key). The hash is `HMAC-SHA256(salt, IP)` (`auth.go:366`), not plain `SHA256(IP)`, so knowing the hash does not reveal the IP and a restart rotates all hashes.
- **Hash length (dynamic):** `hashLen` is computed from live connections `len(Clients)` (`auth.go:360`): `4` chars (16-bit) by default, `5` when `n>100`, `6` when `n>800`, clamped `4..6`. This keeps collision probability low as concurrency grows, while keeping names short when few users are online.
- **Serial for duplicates:** `server/shared.go:DisplayNameCount map[string]int` + `DisplayNameCountMu` tracks active `fullDisplayName`s. If `base = prefix+name+"#"+hash` already exists, the next duplicate becomes `base-2`, then `-3`, etc. (`auth.go:370`). On `unregisterClient` the exact `session.DisplayName` is `delete`d (`clientHandler.go:82`), freeing the slot. The check and claim are `O(1)` and happen once per login.

Final form: `[CustomPrefix]name#hash` or `[CustomPrefix]name#hash-2` (e.g., `[Admin] Alice#a1b2`, `Bob#a1b2-2`). The full `displayName` (including hash and serial) is what is signed in trip messages (`payload = serverPub|seq|prev|msgHash|pub|displayName|tmpID`) and stored in `WireMessage`/`TripMeta`, so a spoofed `displayName` fails trip verification.

Performance: one `HMAC` per login (`µs`), `hashLen` calc is `O(1)` with `RLock`, serial map is `O(1)`. No per-message cost.

## Tripcode

Tripcode is a per-user pseudonym independent from roles, derived from a passphrase.

- Derivation: `salt = sha256(serverPub)[:16]` (or `sha256("V2V-trip-v1")[:16]` if no serverPub), `seed = argon2id(passphrase, salt, t=1/m=32MB WASM or t=3/m=64MB native)`, `ed25519.NewKeyFromSeed(seed)`, `badge = "◆ " + hex(sha256(pub))[:8]`. The passphrase is zeroed after derive; private key is kept in RAM until `/quit` then zeroed. Colors come from a fixed 10-color `38;2` palette rotated by `sha256(badge)[0]`.
- Per-message: `msgHash = sha256(text)`, `payload = serverPub\x00seq\x00prev\x00msgHash\x00pub\x00displayName\x00tmpID` (`internal/tripcolor.CanonicalPayload`), `sig = ed25519.Sign(priv, payload)`, `prev' = sha256(prev|sig|msgHash)`, `seq` strictly increasing per `pub` (enforced via `TripChains` per-pub mutex `TripChainsMu`).
- Transport: `client/tripchain.go:TripMessage` JSON `{text,pub,seq,prev,sig,displayName,tmp_id}` via `WriteJSON`; server `ReadPump` parses it, validates `filter.ValidateMessage(text)`, checks `pub == session.TripPub`, checks `seq == last+1` and `prev == lastPrev`, verifies `msgHash` and `ed25519`, updates chain, stores `WireMessage{Text, TripMeta}`.
- Verification: Both server (`ReadPump`, `history.go:InitHistoryStore`, `trip_api.go`) and client (`verifyCh` FIFO queue, parallel workers, `autoVerify` default on, `/autoverify` toggle) recompute `msgHash` and `Verify`, then recolor badge (`palette` if valid, `91m` red `✗` if tampered). History file edits without updating `sig` are detected as `HISTORY TAMPER`.
- Link: Badge is wrapped in OSC8 `https://<host>/api/trip/verify?pub&seq&prev&sig&msg_hash&server_pub&display_name&text&tmp_id` (stateless `GET /api/trip/verify`, rate-limited `200ms/IP`, capped `2048` query). `serverPub` is enforced to be the server's own key to prevent cross-server reuse. `linkify` (server) and `webterm/app.js` (browser) handle `https` links; `v2v://` legacy is removed.

## Message Rendering

- Every chained message renders as content rows plus exactly one trailing meta line: `|   └─  #height:hash4`, with ` | ✍️ ◆ badge` (hyperlink kept) appended for trip messages. The sender placeholder ends with `|   └─  ··· ⏳` until the echo (carrying the real position) replaces the whole block, so erase counts stay exact. One `renderChatBlock` serves live, echo, history and placeholder paths; legacy lines without chain fields render content only, and pure-local lines (`[Local]`, date banners drawn client-side) carry no meta.

- `codebg.Render` wraps inline `` `code` `` spans and ``` fenced blocks in a background SGR (`48;5;236`, closed with `49m` so ambient foreground survives), stripping the backtick delimiters markdown-style: a ```lang opener becomes a header line showing just the language name (no language means no header), the closing fence is dropped, and single-line ```code``` renders as one background line. Only zero-width escapes are added or removed, so trip `msg_hash` (over raw text) is unaffected; placeholder erase counts are recomputed from the rendered split. Unmatched backticks, empty spans and text containing ESC pass through unchanged.
- `codebg.RenderWithStyle` adds chroma syntax highlighting for fenced blocks with a known language tag (custom per-line SGR emitter, truecolor `38;2` foreground on the configured background, every line self-closed so tab buffers stay consistent). Inline spans, untagged blocks, unknown languages and oversize blocks (>64KiB) fall back to the plain background. Palette comes from `ui.codeStyle` in `config.json` (`background`, `keyword`, `string`, `comment`, `number`, `name`, `function`, `type`, `operator` as `[r,g,b]`; a `[0,0,0]` entry falls back to the compiled default), backfilled by `internal/config` like `tripPalette`. Applied client-side after `SanitizeForDisplay` on incoming chat text; the sender placeholder echo keeps plain `Render` so highlight resets don't cancel its grey wrapper (line counts match either way).
- Applied client-side after `SanitizeForDisplay` on incoming chat text and after `Linkify` on the sender placeholder echo (whole text rendered before splitting, so fence state survives across lines).

## Storage & Persistence

- `data/server_identity.json` — server's long-term Ed25519 keypair, auto-generated, used for `serverPub` pinning.
- `data/history.jsonl` / `.old.zst` — chat history, `zstd` compressed old generation, smart batch `Sync`.
- `data/webauthn.json` — WebAuthn tickets and credentials, `atomicWriteFile` via `CreateTemp+Sync+Rename+dir Sync`.
- `key.json` — encrypted at rest via `XChaCha20Poly1305 + Argon2id` (`version:3` envelope, `chmod 600`, `V2V_PASSPHRASE` env or hidden prompt via `charmbracelet/x/term`, same palette as `v2vctl`).

## Security Model

- **Injection:** All inbound `text` and `username` go through `internal/filter` (`ValidateMessage` rejects `Cf/Mn/Me/Zl/Zp/0xFFFD/non-graphic`, `SanitizeForDisplay` keeps only whitelisted `SGR \x1b[...m` and `OSC8 \x1b]8;;...\x1b\\`). Client double-filters before display, so a compromised server's tampered history cannot execute `ESC[2J` etc.
- **Phishing:** Privileged identities are pinned to `server_pubkey` (not hostname); real passkeys are pinned by `RPID`/`origin`.
- **Spam/Abuse:** `MaxConnectionsPerIP`, `MessageCooldown`, `IdleChatTimeout`, `Trip verify 200ms/IP` rate limit, `SetReadLimit` `64KB` for auth and `MaxMessageLength*3` for chat.
- **Transport:** `REQUIRE_TLS` option blocks `ws://` (returns `426`), `ALLOWED_ORIGINS` checked in `Upgrader.CheckOrigin`. `IsSecuredConnect` trusts `X-Forwarded-Proto` only behind proxy.

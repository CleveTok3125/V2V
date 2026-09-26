# V2V Technical Documentation

This document covers the internal architecture, protocols, and algorithms of V2V.
For a friendly getting-started guide, see [README.md](../README.md).

## Table of Contents
- [Project Structure](#project-structure)
- [Build System](#build-system)
- [Configuration Sync](#configuration-sync)
- [Instances (multiple environments)](#instances-multiple-environments)
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
- [Abuse & Proof-of-Work](#abuse--proof-of-work)
- [Error Handling](#error-handling)
- [Roadmap](#roadmap)

## Project Structure

```
.
├── client/           # CLI and WASM client (shared Go code, platform-specific shims; render.go holds display helpers, client.go the session loop)
├── server/           # WebSocket server, history, auth, WebAuthn
├── internal/
│   ├── identity/     # Shared key file logic (Load/Save, encryption)
│   ├── filter/       # Injection filter (ValidateMessage / SanitizeForDisplay / SanitizeSingleLine)
│   ├── trip/         # Trip verification (Verify)
│   ├── tripcolor/    # Badge color palette + CanonicalPayload
│   ├── chain/        # Global message hash chain (Hash/VerifyLink/genesis)
│   ├── wire/         # Single protocol source (TripMeta/WireMessage/AuthPacket/HistorySync); client/server alias these types, wire_test pins the JSON key set
│   ├── strength/     # Shared zxcvbn policy (bands, weak gate, bit cap) for client + v2vctl
│   ├── strutil/      # One-line log-truncation helper shared by the two package-main binaries
│   ├── passprompt/   # Masked password entry + strength meter (uses tui line readers and TTY probes)
│   ├── guard/        # Pure send/rate/ban/tripcode policy (fully unit-tested)
│   ├── config/       # Client/server config schema + defaults
│   ├── trustedproxy/ # Explicit proxy chain (none/cloudflare/direct) + trust-file loading
│   └── configdir/    # XDG-aware default dirs
│   └── tui/          # General huh confirms/selects + piped fallbacks
│   ├── markup/       # Forum markdown facade over codebg + linkify
│   ├── linkify/      # URL → OSC8 hyperlink
│   └── codebg/       # inline `code` + ``` blocks → background SGR + chroma highlight (display only)
├── webterm/          # Browser terminal (xterm.js + WASM glue)
├── cmd/v2vctl/       # Management tool, one file per concern (main, role, keygen, enroll, migrate, list, prompt, config, pager)
├── template/         # Samples mirroring real locations
│   ├── server/instances/default/  # instance template: .env + config/{roles.json,trustedproxy/}
│   └── client/         # config.jsonc + key.json → copy to OS config dir
├── instances/        # Gitignored: one dir per environment (default: default)
└── docs/             # This file
```

## Build System

All builds are driven by `Makefile`:

```bash
make help            # list targets
make vet test        # go vet/test (GOCACHE defaults under TMPDIR, then HOME)
make dev             # dev build: bin/v2v, bin/v2v-server, bin/v2vctl + fresh webterm (unstripped, dev-<hash> stamp)
make -j4 all         # parallel: server + web + client + v2vctl (host only for client/v2vctl)
make all ALL=1 -j4   # cross 6-platform matrix for client/v2vctl (android: NDK, see below)
make server          # public/server.bin (-tags netgo, -trimpath)
make web             # webterm/app.wasm (+ wasm_exec.js, version.js, gzip/br)
make client          # host only: public/V2V-$(go env GOOS)-$(go env GOARCH)
make client ALL=1    # cross matrix: public/V2V-* (6 platforms)
make v2vctl          # host only
make v2vctl ALL=1    # full matrix
make clean
```

- Version stamping: `APP_VERSION` prefers `GIT_HASH`, else `git describe --tags --always`; injected via `-ldflags -X 'main.Version=...'`. `make web` stamps `version.js` from `VERSION` (also `GIT_HASH`-first), so server, client, v2vctl and webterm share one tag.
- Cross-compile: `CGO_ENABLED=0 GOOS=... GOARCH=... go build -trimpath`; host OS detected via `go env GOOS/GOARCH` (`HOST_GOOS/HOST_GOARCH`).
- Default `make client`/`v2vctl` builds only host binary for fast dev; `ALL=1` builds the cross matrix (6 platforms). Android is not cross-compiled here: it needs the NDK + cgo (next bullet), so the release builds it separately.
- Web assets: the server resolves the `webterm/` directory next to its executable, then falls back to `./webterm`; `WEBTERM_DIR` overrides both. The wasm page and the `/web/` statics follow it, so a release binary serves its bundle from any working directory.
- CI: `.github/workflows/ci.yml` runs vet, tests, entrypoint tests (sudo) and the wasm tests on push to `main/master` and PRs (Go 1.27, cache).
- Release: pushing a `v*` tag runs GoReleaser (`.goreleaser.yaml`). It builds the client and v2vctl matrix (7 platforms), the server matrix (linux/darwin/windows, amd64/arm64), and one `V2V-server-<os>-<arch>` archive per server platform bundling the server binary, the wasm bundle, the bootstrap `template/`, `docker-compose.yml` and the matching `v2vctl`. The android client is built separately with `CGO_ENABLED=1` and the NDK toolchain (`nttld/setup-ndk`, `CC_ANDROID`): a `CGO_ENABLED=0` android build uses the pure-Go resolver, which reads `/etc/resolv.conf` (absent on Android) and falls back to `127.0.0.1:53`, so DNS fails on Termux; cgo uses the bionic resolver instead. It then writes `SHA256SUMS`, generates SBOMs, signs the checksums with cosign (keyless) and creates a draft GitHub release (auto-prerelease for `-wip`/`-beta`/`-rc` tags). The same tag builds and pushes the multi-arch image (`linux/amd64`, `linux/arm64`) to GHCR with `docker/build-push-action`.
- Docker: `Dockerfile` cross-compiles the server on the host-platform builder (`--platform=$BUILDPLATFORM`, `TARGETOS`/`TARGETARCH`) and runs `make web` once (the wasm bundle is platform-independent); the runtime image ships `server.bin` + `webterm/` and defines a `/api/version` healthcheck.
- Dev version stamp is always `dev-<HEAD>[-dirty]` from the working tree, never from a possibly stale `GIT_HASH` env; `make web` warns when `GIT_HASH` differs from `HEAD` (stale browser cache risk).
- Version stamps ride `-X main.Version` for all three binaries (server included); server prints its stamp at boot, on `/` info and on `/api/version` (`{"version": ...}`, no-store).
- Pre-dial version check is client-side policy (`ui.versionCheck {enabled, mode, expect}`): exact string match against our stamp or a pinned fork version, modes `disabled|warn|enforce` (default warn; unknown warns, enforce aborts non-zero), WASM skipped (paired with its server).

## Configuration Sync

`v2vctl config sync` migrates the template into a live config, preserving operator-set values. The merge is template-first: the template wins structure, order and comments; the live config wins values per key.

- **Manifest:** the project root holds a tracked `v2v-template.json` (`version`, `type`, `templateDir`, `files[]`, `add_policy`). Each file entry declares `id`, `path` (template-relative), `format` (`env|json|jsonc|trust-dir`), `dest` (config-relative), optional `target: "client"` (dest is under the OS config dir) and `keys` (expected key order). `--dir` points at the directory holding the manifest (default `.`); `--to` is the instance root (default `instances/default`, or `V2V_ROOT`). Missing or invalid manifest fails the run; there is no fallback.
- **Commands:** `config sync` (merge + write), `config diff` (unified diff preview, `--format text|json`), `config check` (manifest vs template drift), `config manifest --write` (regenerate `keys` while preserving `add_policy`). `--only` selects a subset of manifest ids (default `env,roles,trust`; `client` is opt-in).
- **Render:** `.env` and trust files are walked line-by-line so comments, blank lines and quoting stay byte-identical; a commented `#KEY=value` default is activated in place when the operator enables it; template JSON is re-indented with the template's own indent unit and key order. Merging the template with itself reproduces it byte-for-byte.
- **`add_policy`:** keys that must not inherit the template value when newly added. `env.comment` renders them commented (`#KEY=value`); `roles`/`jsonc` `skip` omits them. Only the added branch is affected. The list is for operator-supplied values only: a key whose shipped value is a handled sentinel (e.g. `PROXY_PROVIDER=false`, which fails the boot with an actionable message) stays active, and listing it here would hide that message behind a comment.
- **Guards:** the config root and every destination must stay outside the template root; malformed template/local JSON is refused instead of being overwritten. JSONC targets are lossy (comments and formatting are normalized), so `sync` requires `--force` for such entries after reviewing `diff`.
- **Take (`--prefer-template`):** inverts the merge for designated parts only: the template wins values for `id` (whole file: every template-known key) or `id:key` (single key), comma-separated and repeatable (e.g. `--prefer-template env:PORT,env:BIND_ADDR`). Unknown ids/keys fail closed. Local-only keys are never dropped. Trust-dir takes whole files verbatim and refuses when the template file has no entries. An explicit take beats `add_policy` skip/comment. Writes with a non-empty take need `--yes` after the mandatory diff preview (`diff`/`--dry-run` preview without writing); the report lists reset keys under `taken`.
- **Modes:** config artifacts are written `0644` (dirs `0755`) via `identity.WriteConfigFile`; secrets keep `0600`/`0700`.

## Instances (multiple environments)

One binary serves many instances: an instance is a directory holding `.env`, `config/` and `data/`. The instance root is `V2V_ROOT` (default `instances/default`), resolved before the `.env` load — so the variable must come from the process environment, never from the instance `.env` itself.

- Host: `v2vctl config sync --dir . --to instances/<name>` seeds an instance; run it with `V2V_ROOT=instances/<name> ./public/server.bin`. `v2vctl role`/`enroll`/`list` accept `--root` (or `V2V_ROOT`) and default to `instances/default`. The instance template lives at `template/server/instances/default/`.
- Tooling: `v2vctl instance init <name>... [--port N] [--bind A]` (creates `.env` + `config/` via config sync and an empty `data/`), `instance list`, `instance status [<name>...]` (no name = all), `instance up [--build] <name>...`, `instance down <name>...`, `instance restart <name>...`, `instance logs [-f] <name>`. Batch commands run every name and report each failure; only `logs` stays single-instance because `-f` blocks on the first. The wrapper shells out to `docker compose -p v2v-<name> --env-file instances/<name>/.env` and exports `ENV_ROOT=instances/<name>`; there is no preset and no override file.
- Each instance has its own chain, `server_identity.json`, `webauthn.json`, `roles.json`, history and logs; `V2V_ROOT` only changes which directory they hang off. Absolute per-artifact overrides (`DATA_DIR`, `LOG_FILE_PATH`, `HISTORY_FILE_PATH`, `TRUSTED_PROXY_DIR`, `WEBAUTHN_STORE`) still win.
- Container: the compose service mounts `${ENV_ROOT:-instances/default}/.env`, `.../config`, `.../data` into `/app` and pins `V2V_ROOT=/app`. Multiple instances run from the same image with `ENV_ROOT=instances/<name> docker compose -p v2v-<name> up -d --build` (no fixed `container_name`).
- Migration from the old root layout: `mkdir -p instances/default && mv .env instances/default/.env && mv config instances/default/config && mv data instances/default/data`.
- **Upgrade note (layout):** `config sync` now defaults `--to` to the instance root (`instances/default`) and the server reads `<V2V_ROOT>/.env` with no root-layout fallback, so old root-layout deployments must migrate (or keep working in place with `V2V_ROOT=.` and `config sync --dir . --to .`).

## Client Configuration

- Locations follow the OS (`internal/configdir`): config dir holds `key.json` + read-only `config.jsonc` (`~/.config/V2V/` Linux, `%AppData%\V2V` Windows, `~/Library/Application Support/V2V` macOS); cache dir holds `history.tmp`. Config is JSONC (`//` and `/* */` comments allowed, no trailing commas), never written by the app (missing file means in-memory defaults), and optionally passphrase-sealed with the identity envelope (`v2v --encrypt-config`, `V2V_PASSPHRASE` or TTY prompt to unlock).
- Override with `-c/--config-dir` (`V2V_CONFIG_DIR`) and `-C/--cache-dir` (`V2V_CACHE_DIR`).
- Identity flags: `-k` uses the default key in the config dir, `-K/--key-file <path>` uses an explicit path (old `v2v -k <path>` now errors). No key given means guest mode.
- `template/client/config.jsonc` documents every group (`defaults`, `network`, `limits`, `guard`, `channels`, `crypto`, `ui`, `commands`, `timeouts`, `tabs`); `internal/config` loads it with `LoadOrCreate`.
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

- Dispatch matches exact tokens (`/help`, `/quit`, …), `/tab`/`/t` with optional `1|2`, `/meta`/`/m` with optional `on|off`, `/find`/`/f` with `<height>[:hash]`, `/info` with `<height>[:hash]` and `/expand`/`/xpan` with `<height>`.
- Session commands: `/whoami`/`/w`, `/status`, `/showjoin`/`/sj`, `/autoverify`/`/av`, `/clear`/`/c`, `/clearhistory`/`/ch` (deletes the keystroke history file), `/copy <height>[:hash]` (clipboard, auto-cleared).
- Anything else starting with `/` is an unknown command (`client/commands.go:isUnknownSlashCommand`) rejected locally with `| [Local]: Lệnh không tồn tại…`, never broadcast or trip-signed.
- Code blocks (```) are unaffected, so they double as the escape hatch for sending literal text starting with `/`.
- `/reply <height>[:hash] <text>` quotes a buffered message; the height suffix acts as a typo checksum.
- Bare `/reply <height>` opens a draft: the quote previews at once and the next line becomes the body (any `/` command or empty-line `^C` aborts); only chat messages are quotable, never server markers.
- Quotes resolve per receiver (`wireIdx` first for time/author, buffer-head fallback) and render `↩ #height | time author: text…` in placeholder and echo alike; a chain-content mismatch appends a red `✗`, intact content shows no mark. The verdict recomputes the target's chain content locally.
- `/info <height>[:hash]` prints the full metadata detail of one indexed wire (height/tmp/reply, full hashes, trip fields with live signature verdict, chain verdict, plus a `raw:` row with stored bytes unrendered: newlines as `⏎`, ESC/control dropped).
- Long blocks fold at render (`ui.collapse {enabled, rows, previewRows}`, defaults on/10/5): over-threshold heads print preview rows with dim `...` plus `[Xem thêm: /expand #height]` on the last preview line (OSC8 `v2v://expand/<height>` link on web, clicked via Ctrl+U-clean command injection). `/expand <height>` re-renders the full block from the wire index and replays it inside a git-style conflict frame (`| [Local]: <<<<<<< #height` … `| [Local]: >>>>>>> #height`, markers dimmed) — no buffer surgery, no cursor math. Evicted wires report as drifted; system-tab content never folds.
- The trip section shows every signature input (pub, seq, prev, sig, msg_hash with text-match mark, server_pub, payload bytes), so the verdict is checkable by hand with any ed25519 tool.
- Wires index by height (cap 1000 FIFO); legacy and evicted report as missing.
- `/find` looks up messages by chain height (the mandatory identifier; a bare hash is rejected since short hashes collide by design, and an appended `:hash` acts only as a typo checksum) across both tab buffers; hits replay inside the same git-style conflict frame as `/expand` (`| [Local]: <<<<<<< #height` … `| [Local]: >>>>>>> #height`, markers dimmed, suffix omitted from the marker), so replayed lines never read as live chat. Evicted history reports as not found with no frame.
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
- Rotation: when `size > MAX_HISTORY_FILE_SIZE_MB` (`50MB` in `template/server/instances/default/.env`), current file is renamed to `.old` and compressed to `.old.zst` via `klauspost/compress/zstd` (`50MB → ~3MB`).
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
- Key file `key.json` (`internal/identity/identity.go`) is a versioned container (`version:3`) with one `ed25519` slot.
- `Ed25519Identity` stores `role`, `private_key` (hex 128), `hmac_shield` (hex 32), `server_pubkey` (hex 64, from `data/server_identity.json`).
- Handshake: server sends `auth_challenge {nonce, serverPubkey, serverSig}` where `serverSig = ed25519.Sign(serverPriv, "V2V-SERVER-v1\x00"+nonce+"\x00"+host)`.
- Client verifies `serverSig` against `serverPubkey` pin (or `server_pubkey` in `key.json`), warns on mismatch, then signs `dataToSign = nonce|role|username|serverPub` with its private key.
- Client sends `signature` + `hmac = HMAC-SHA512(signature + nonce, hmac_shield)`. Server verifies `ed25519` and `hmac.Equal`, checks `ServerPubKey` pin, and enforces `TripChains` for trip users.
- `HMAC` with `bytes(signature)` prevents replay without the shield even if private key is exposed.

### Passkey (WebAuthn, ceremony-only)
- Software self-minted passkeys and paste import are deleted: credentials enter only via ticket ceremony (`v2vctl enroll --role member` → `/webauthn/enroll/begin` → `navigator.credentials.create` → `/webauthn/enroll/finish`), stored in `data/webauthn.json` v2 (`WebAuthnStore`; v1 refused at boot with a re-enroll message).
- Verification runs 100% through go-webauthn (`ValidateLogin`/`CreateCredential`): challenge, origin, RP ID, flags, signature, counter, ES256-only credential params. Hand-rolled verification and the manual COSE parser are deleted.
- Hard policy: user verification required, ES256-only, credential IDs clamped to 1023 bytes, claimed ID must match the parsed credential. Attestation is requested as `direct` but any format (including `none`) is accepted and recorded per credential — many software and synced passkey providers do not provide a vendor attestation chain usable for RP provenance checks, so format is recorded metadata, not a gate.
- Attestation chains are intentionally not verified: many software and synced passkey providers do not provide a vendor attestation chain usable for RP provenance checks, and V2V policy does not require provenance — so chain-of-trust would lock out common authenticators without adding anything over ceremony binding + UV + ES256 + counter. MDS is out of scope by design, not by omission.
- Enrollment hardening: per-IP begin cooldown, single-bind challenge per ticket (failed ceremony needs a reissued ticket), duplicate credential IDs rejected. Enrollment must run over TLS (`REQUIRE_TLS`); tickets are single-use with short TTL.
- Login verifies the counter against the managed store (clone detection); soft-key counter exemptions no longer exist because soft keys no longer exist.
- Authenticator backup flags (BE/BS) are recorded at enrollment and replayed into login verification: the library rejects a backup-state mismatch, so zeroed stored flags would fail every synced-provider login. Store is v3; older stores are refused with a re-enroll message.
- Breaking change: all pre-rebuild credentials (soft key.json slots, roles.json `passkeys[]`, v1 store) are rejected — re-enroll every passkey.
- RP binding is a single pair: `RPID: WAConfig.RPID` and `RPOrigins: []string{WAConfig.Origin}`, so exactly one origin can run the ceremony. Serving the web client from a different host (e.g. the standalone `serve.py` bundle on `127.0.0.1`/`localhost` over HTTP) disables passkey there; guest, tripcode and ed25519 key logins are unaffected. Passkey on such a page requires serving it from the exact `WEBAUTHN_ORIGIN` hostname over HTTPS, which cannot also serve another production origin.

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

Paths below are relative to the instance root (`V2V_ROOT`, default `instances/default`); absolute per-artifact env overrides still win.

- `data/server_identity.json` — server's long-term Ed25519 keypair, auto-generated, used for `serverPub` pinning.
- `data/history.jsonl` / `.old.zst` — chat history, `zstd` compressed old generation, smart batch `Sync`.
- `data/webauthn.json` — WebAuthn tickets and credentials, `atomicWriteFile` via `CreateTemp+Sync+Rename+dir Sync`.
- `key.json` — encrypted at rest via `XChaCha20Poly1305 + Argon2id` (`version:3` envelope, `chmod 600`, `V2V_PASSPHRASE` env or hidden prompt via `internal/passprompt` on TTY / `charmbracelet/x/term` piped fallback, same flow as `v2vctl`).
- Identity writes (`key.json`, `roles.json`) go through `renameio/v2/maybe`: atomic temp-file + fsync + rename on Unix, plain `os.WriteFile` fallback on Windows where atomic replace is not reliably available. The requested `0600` passes through the process umask on v2 (v1 ignored it), and an existing regular file keeps its own permissions.
- The successful unlock secret is remembered for the session so counter saves re-encrypt instead of dropping to plaintext, and wiped at exit (`ClearLoadedPassphrase`, deferred plus the conn-drop path).
- File-supplied argon2 costs are clamped (t 1-10, m 8-256MiB, p 1-8) and the envelope identity (v3/argon2id/xchacha20poly1305) verified, so crafted files fail closed instead of exhausting RAM.
- **No-content-logs mode (`NO_CONTENT_LOGS=true`):** content-privacy, not "zero logs". Chat history stays in RAM only (`Chain.Store` is nil, `HISTORY_FILE_PATH`/`LOG_FILE_PATH` are ignored, no rotation and no disk lookup), and message content is never logged: the per-message line is dropped and rejected payloads are recorded by error type only. Operational metadata still goes to stdout/stderr — client IP, auth/identity events, admin and error lines — and identity/auth files (`server_identity.json`, `webauthn.json`, `config/roles.json`) are still written. Pre-existing history/log files are left untouched and only warned about; full no-content-retention also requires disabling OS-level log persistence (e.g. journald).

## Security Model

- **Injection:** All inbound `text` and `username` go through `internal/filter`.
- `ValidateMessage` rejects `Cf/Mn/Me/Zl/Zp/0xFFFD/non-graphic`, `SanitizeForDisplay` keeps only whitelisted `SGR \x1b[...m` and OSC8 links (`\x1b]8;...;uri\x1b\\`) whose target is `http(s)` or the client-generated `v2v://expand/`; every other OSC (clipboard write, palette/color, kitty) is dropped, and an unterminated OSC drops the tail instead of leaking the link target.
- Client double-filters before display, so a compromised server's tampered history cannot execute `ESC[2J` etc.
- Server-controlled fields outside message `text` are sanitized at their render sink: `time`/`display_name` through `SanitizeSingleLine`, free text (server host, auth error, dial response body, `--info` page) through `SanitizeForDisplay`, chain/trip hex only when it really is hex, and all trip-verify URL parameters percent-encoded. The same filter backs the WASM web client.
- **Client DoS bounds:** the desktop client sets a WebSocket `SetReadLimit` of `maxHistoryBytes + 1MiB` (the server sends replay lines individually, so every legitimate frame is far smaller) and caps the `--info` body at `256KiB`; the WASM shim enforces the same per-frame budget before queueing.
- Remaining exposure sits in the terminal emulator itself (outside the client) and in OSC8 phishing via an allowed-scheme link the user chooses to open.
- **Phishing:** Privileged identities are pinned to `server_pubkey` (not hostname); real passkeys are pinned by `RPID`/`origin`.
- **Spam/Abuse:** `MaxConnectionsPerIP`, `MessageCooldown`, `IdleChatTimeout`, `Trip verify 200ms/IP` rate limit, `SetReadLimit` `64KB` for auth and `MaxMessageLength*3` for chat.
- **Config validation:** `v2vctl config validate` reuses the boot loader (`internal/serverconfig`, shared by the server and the tool) to report every fail-closed problem before a start. Most variables are required (`Smart`/`Int`/`Duration`: missing or bad format is fatal); the display URLs (`STATUS_URL`, `DOWNLOAD_URL`, `HOMEPAGE_URL`) are optional and their status lines are simply omitted when empty. `PROXY_PROVIDER` ships as the sentinel `false`, so the default `.env` fails validation with the actionable `ParseChain` message rather than a bare "missing variable".
- **Transport:** `REQUIRE_TLS` option blocks `ws://` (returns `426`), `ALLOWED_ORIGINS` checked in `Upgrader.CheckOrigin`; the code default is `true` (fail-closed when the var is missing).
- `IsSecuredConnect` trusts `X-Forwarded-Proto` only from a header-trusted proxy (see below), never from direct clients.
- **Onion (Tor) ingress:** `ONION_HOSTS` lists accepted `<id>.onion` names (normalized, empty disables; a non-`.onion` entry fails the boot). A request to a listed host is treated as already encrypted when it arrives from a loopback address or from a hop listed in `config/trustedproxy/onion.txt` (dedicated to onion; `direct.txt` grants no onion power); a header-trusting proxy never qualifies. `REQUIRE_TLS` stays `true` for clearnet. Onion clients have no client IP, so all onion traffic shares one abuse bucket — raise `MAX_CONNECTIONS_PER_IP` for onion deployments. Passkey and the WASM web client are off for onion unless `ONION_ALLOW_PASSKEY` / `ONION_ALLOW_WEB` are set (a passkey binds a single RPID, so one server cannot serve clearnet and onion passkeys at once); the API and WS chat (guest/tripcode/ed25519 key) stay available. Serving the web client is two layers: `WEB_ENABLED` (default `true`) is the master switch for every request, and `ONION_ALLOW_WEB` (default `false`) adds the onion restriction, so an onion request needs both; the WASM asset is served by the same binary, which gives a browser deployment the browser sandbox with no separate web server.
- **Upgrade note (REQUIRE_TLS):** the code default changed to `true`, so an existing `.env` without `REQUIRE_TLS` now rejects plaintext `ws://` (`426`) for non-loopback, non-proxy clearnet clients. Set `REQUIRE_TLS=false` explicitly to keep the old behavior where that is intended.
- **Trusted proxy (explicit chain):** `PROXY_PROVIDER` (required, e.g. `cloudflare,direct`) selects modules from `internal/trustedproxy` (`none`, `cloudflare`, `direct`); only `none|cloudflare|direct` are supported. Trust comes solely from per-module `<name>.txt` files under `TRUSTED_PROXY_DIR` (default `./config/trustedproxy`): no embedded IP defaults, no implicit fallback.
- Resolution per request: trusted edge → provider header (`CF-Connecting-IP`, validated); listed `direct` IP → `RemoteAddr` (never reads headers, so a misplaced entry is harmless); chain with `none` → `RemoteAddr` with a warning; otherwise reject `403` with the received headers logged (clipped). Missing `cloudflare.txt`, malformed entries, or an empty `PROXY_PROVIDER` fail the boot on purpose.
- Boot prints the active trust (providers, per-file range counts, direct policy); each connect logs `clientIP | RemoteAddr | provider | trusted`.
- Stray `.txt` files fold into one bounded warning line and directory scans are capped, so a directory dump cannot flood the logs.
- **Cloudflare Tunnel / Docker NAT:** the container never sees the CF edge IP; `RemoteAddr` is the tunnel/NAT hop (e.g. `172.18.0.1`), so a strict `cloudflare` chain rejects with `⛔ [PROXY] Reject <hop> (no trusted provider)` even though `CF-Connecting-IP` is present. Fix: add the hop range (e.g. `172.18.0.0/16`) to `cloudflare.txt`, and make the tunnel the sole ingress — compose binds the port to loopback by default (`BIND_ADDR`, `127.0.0.1`), so either keep it or drop the `ports:` block (cloudflared reaches the app by service name on the internal network). Set `BIND_ADDR=0.0.0.0` only for direct exposure behind a CF-whitelisted host firewall: published-to-internet ports let direct-to-host traffic arrive with the same hop IP and spoof headers.
- Variant matrix: no proxy → `PROXY_PROVIDER=none`; behind CF (edge IPs visible) → `cloudflare` + published ranges; behind Tunnel/NAT → `cloudflare` + hop range + sole-ingress as above; office/health-check direct IPs → `direct` list (never header-trusted); onion hidden service → `PROXY_PROVIDER=none` (loopback tor) or `none`/`direct` with the tor hop in `config/trustedproxy/onion.txt` + `ONION_HOSTS` + the onion origin in `ALLOWED_ORIGINS`.
- Verify: `docker ps` shows no `0.0.0.0` mapping (or none at all), outside `curl http://<host>:<port>` is refused, the CF domain works, and logs show `Via: cloudflare trusted=true` with no more `403`.
- **Outbound proxy (desktop client):** `--proxy URL` / `V2V_PROXY` env beat the system `HTTP(S)_PROXY`/`NO_PROXY` (gorilla `DefaultDialer` still honors those when nothing is set).
- `--ask-proxy` runs an interactive wizard (huh scheme select, host, port, optional user, hidden password) that overrides all static config.
- HTTP(S) proxies use a dedicated gorilla `Dialer`; SOCKS5 handshakes via `golang.org/x/net/proxy` (RFC 1928/1929) with the target always sent as a domain name so no local DNS leaks, plus TLS for `wss`.
- The proxy password is the proxy's secret: no meter, no weak gate. It lives as `[]byte`, wipes after dial, and logs show `user:***@host`; prompt/URL strings at the stdlib boundary await GC as documented for all secrets.
- **File exposure:** secrets and chat land owner-only — `key.json`, `webauthn.json`, `server_identity.json` via `atomicWriteFile 0600` (dirs `0700`); `history.jsonl` and `app.log` via `0600` (`history` dir `0700`). Server config artifacts (`roles.json`, `.env`, `config/trustedproxy/*.txt`, client `config.jsonc`, `v2v-template.json`) use `WriteConfigFile` `0644` (dirs `0755`) so container bind mounts stay readable when host and container uids differ; this makes `roles.json`'s `hmac_shield` host-readable, matching the `cp -n` bootstrap that always produced `0644`. `OpenFile` applies the mode only at creation, so pre-existing files keep theirs: re-harden once on the host with `chmod 700 data` and `chmod 600 data/history.jsonl* data/app.log* data/server_identity.json data/webauthn.json`.
- Missing `config/roles.json` fails the boot (no silent default-permission fallback); a corrupt file fails too, while hot-reload keeps the old registry with a warning.
- Trust files refuse to load when world-writable; templates ship no secrets (placeholders only).
- **Container:** the image builds with live `data/`/`config/`/`.env`/`instances/` excluded (`.dockerignore`), runs the server as a non-root `app` user via `docker/entrypoint.sh` (root prepares `$V2V_ROOT/data` idempotently, preflights the read-only mounts with actionable errors, then `exec su-exec`), drops all capabilities except `CHOWN`/`SETUID`/`SETGID` (needed by that root phase), blocks privilege escalation and mounts the rootfs read-only (`/tmp` tmpfs). No `user:` is set on purpose — the entrypoint adapts, rollback is commenting out `read_only`. Compose mounts one instance (`ENV_ROOT`) into `/app` and sets `V2V_ROOT=/app`.

## Abuse & Proof-of-Work

Self-contained mass-impact defense (works with `PROXY_PROVIDER=none`, no
external services): global connection cap, operator blocklist, optional
IPv4-only, an under-attack state machine, a PoW join gate, dynamic
behavior scoring with in-chat PoW screening, and container bounds. All
abuse knobs are required in `.env` (group A always, group B when
`BEHAVIOR_ENABLED=true`); `v2vctl config validate` fails naming any
missing one. Defaults live only in `template/.env`.

- **Global cap:** `MAX_TOTAL_CONNECTIONS` (default 500) counts registered
sessions plus in-flight handshakes (`Inflight`, atomic). `ServeWS`
answers `503+Retry-After` past it before `Upgrade`; `registerClient`
re-checks against the same bound so a burst cannot overshoot it.
- **Blocklist:** `BLOCKLIST_FILE` (default `./config/blocklist.txt`, one
IP/CIDR per line, `#` comments, same parser as trust files) matches
before any rate limit and answers `403`. A missing file only warns;
malformed or world-writable files fail the boot.
- **IPv4-only:** `REQUIRE_IPV4=false` rejects non-IPv4 client addresses
(v4-mapped counts as v4) with `403`.
- **Log clipping:** attacker-controlled payloads in reject lines are cut
to 512 runes (`trustedproxy.Clip`), so one request stays one line.
- **Under-attack machine** (`server/underattack.go`, 1s sampler): enters
when the cap holds for `ATTACK_ENTER_SECS` or the 5s reject rate passes
`ATTACK_ENTER_RPS`; holds the full `UNDER_ATTACK_TTL` with re-arm while
hot and exits only after a quiet TTL (`UNDER_ATTACK_FORCE=auto|on|off`).
Attack magnitude (`scale ∈ [0,1]`) folds reject ratio, connection-rate
growth vs an EWMA baseline, new-IP growth and blocklist hits
(`ATTACK_SCALE_MODE=max|weighted` + weights).
- **Join gate** (`POST /api/join-gate`): `step=challenge` issues a PoW
bundle (tier preset, bound salt, `earliest=now+rand(JOIN_WAIT_MIN,MAX)`,
HMAC ticket, `POW_TTL` expiry, signed offer); `step=submit` verifies
ticket, clock (early answers get `429+Retry-After` without consuming
the bundle) and PoW (wrong answers consume it) and returns an IP-bound
lease pass (`PASS_TTL`, reusable unless `PASS_SINGLE_USE`). `ServeWS`
requires `?gate_pass=`/`X-V2V-Pass` when `POW_FIRST_CONNECT=always`, or
only while under attack (`under-attack`, default). The gate check runs
before the connection cooldown so a rejected pre-gate dial never stamps
it. Challenge issuance is throttled per IP (2s, internal).
- **PoW** (`internal/pow`): one argon2id (`t/m/p` per tier, memory-hard)
plus a sha256 leading-zero search (`difficulty` bits), so the server
verifies with one argon2id plus one hash. Presets come from
`POW_P{n}_T/M/P/DIFF/EST_MS` (tier 0 = no PoW), clamped to `T1-10`,
`M8-256MiB`, `P1-8` with wasm forced to `P=1`. Offers are ed25519-signed
(`V2V-POW-v1`) over tier+preset+salt+expiry+id; clients verify against
the pinned server pubkey before spending work.
- **In-chat screening:** a 30s scheduler re-scores connected IPs on a
randomized `POW_RECHECK_MIN/MAX` cadence and challenges only flagged
sessions (tier ≥ 1): a per-session `[Hệ thống]:` notice (Tab 2) explains
the check, then a signed `pow_offer` frame. Answers ride `pow_result`;
`pow_decline` (or silence past `max(SCREEN_DEADLINE, IdleChatTimeout)`)
mutes chat sends while reading stays open; overdue challenges kick only
while under attack. Wrong solutions are consumed to bound verify cost.
- **Behavior scoring** (`internal/behavior`, metadata timing/count only,
never content): per-IP profiles (event rings, counters) feed 13
normalized features — rhythm (CV of gaps), long-window throughput,
deep-night share, continuity, connect churn, identity cost, IP
reputation, protocol/auth/envelope anomalies, HTTP rate/errors/endpoint
focus. `score = (Σw·f/Σw)^γ` with per-feature weight/floor/ramp
(`W=0` disables); no evidence ⇒ clean IP scores 0 (cold start stays
tier 0), blocklist hit scores 1. Tiers move on hysteresis
(`ENTER_k>EXIT_k>ENTER_{k-1}`); the lowest tier needs no PoW.
- **Grouping:** subnet (`/24`, IPv6 `/64`+`/48`), ASN, country/region keys
combine member scores (worst + mean, weighted by level) so distributed
rotation inside one netblock still taints. GeoIP is optional
(`BEHAVIOR_GEOIP_DIR` with `GeoLite2-ASN/City.mmdb`, operator-supplied,
missing files only skip those levels).
- **Persistence & stats:** profiles persist like history (path
`BEHAVIOR_FILE_PATH`, per-tier retention, atomic rewrite, SIGTERM
flush); a separate aggregate tier histogram (no per-IP data) snapshots
every `BEHAVIOR_STATS_WINDOW` for calibrating weights. `BEHAVIOR_ENABLED=false`
disables scoring, in-chat PoW, stats and GeoIP entirely.
- **HTTP gate:** heavy classes (`GATE_HTTP_CLASSES`, default
trip_verify/webauthn) require the reusable lease pass while under
attack (`GATE_HTTP_MODE`, tier mode gains per-IP checks with scoring);
`verify.html` fetches a pass before its JSON call. `trip_api` reads the
page only after the query cap, proxy reject and per-IP cooldown.
- **Client** (`pow` section in `config.jsonc`): `maxTier`/`maxCostMs`
bound what the client will solve; anything beyond is declined. The
join flow dials, and on a 429 gate challenge solves PoW in a worker
goroutine (desktop; WASM runs it on the main thread with the server
notice explaining the freeze), waits out the ticket and redials with
the pass (bounded retries). In-chat offers verify against the pinned
server identity, clamp to platform and budget, then solve in
background and answer over the socket (writes serialized).
- **Container bounds** (`docker-compose.yml`): `nofile` 8192,
`pids_limit` 512, `mem_limit` 512m, `cpus` 1.0, json-file logs
(`10m`×3), `stop_grace_period` 30s — on top of the existing healthcheck
and `STOPSIGNAL`. The app cap sheds load first; these keep a flood from
exhausting host fds, threads, RAM or disk.

## Error Handling

- Return paths wrap with `%w` so callers can `errors.Is`/`errors.As`: sentinel auth errors (`server/auth_errors.go`, byte-identical legacy text), `config.ErrEncrypted`, env parse errors up to the boot fatal.
- Terminal sinks use `%v`: `logInfof`/`logWarnf`/`logErrorf` and test failures never unwrap further, so `%w` there would only mislead.
- `log.Fatal` stays reserved for process death at boot (`server/main.go`); everywhere else degrades or returns the error.

## Roadmap

Planned work grouped by dependency, in recommended order. Each item stays self-contained: it can land without the others, but the order avoids rework.

### Phase 0 — Foundation

- **Session surgery** — client split into display/chain/verify/pending groups with a fixed `Display -> Chain -> Pending` order (tip persist outside the lock, queue-only pending guards); server split into `ChainService` (tip, history, store) and `Hub` (presence and send ordering) with the lock order unchanged. — *DONE*
- **Dependency arrows** — `markup` is the sole facade (`Style` alias, `DefaultStyle`, `NeedsContinuation`, `Linkify` passthroughs): `client` no longer imports `codebg`/`linkify` directly; `strength` owns its report type and `guard` takes a plain limits struct are done. — *DONE*
- **Env/log/error unification** — typed getenv helper (`internal/env/env.go:20`) and leveled logging (`server/loglevel.go:10`) are done; the `%w` rule lives in [Error Handling](#error-handling). — *DONE*

### Phase 1 — Evidence

- **First audit producer** — `BroadcastAudit` (`server/history.go:214`) has tests but no callers; wire the first management action through it: rank-gated kick first (actor rank must exceed target; duration 0 = kick, longer = ban), audit text never carries IPs. — *BACKLOG*
- **Paged history** — fetch older segments on demand over the authenticated WS (`history_request` with `before`/`limit`, answered in replay format with a `history_sync` trailer, RAM window plus opt-in disk tiers); the connect-time replay (`MAX_HISTORY_SEND`) stays a join burst for fast startup. Client `/older [n]` pages below the oldest height in memory. Disk tiers (`HISTORY_DISK_LOOKUP` 0–3: off, active, raw, archive) stream older generations with a boot-built sparse height index; per-session `HISTORY_SEGMENT_COOLDOWN` throttles requests. The tail window (`before=0`) always serves from RAM, never disk. — *DONE*

### Phase 2 — Blog (frontend done, backend parked)

Frontend (`server/blog/render.go`, `webterm/blog/cactus.css`) shipped; backend parked. In order when resumed:

- **Blog permission** — `CanManageBlog` in `wire.Permission` + role template + `v2vctl` flags. — *BACKLOG*
- **Blog store** — `DATA_DIR/blog/{slug}.md` + sidecar JSON (slug `[a-z0-9-]`, 3 fixed tags, atomic write, read cache). — *BACKLOG*
- **Blog management auth** — mirror WS auth (ed25519/passkey + nonce/IP cooldown, ed25519 first). — *BACKLOG*
- **Blog routes** — `GET /blog/`, `/blog/{slug}`, JSON `/api/blog/*` + rate limits. — *BACKLOG*
- **CLI raw read** — `v2v --blog [slug]` prints raw markdown (`?format=raw`), pipes to `glow`/`mdcat`, TTY pages via `$PAGER`. — *BACKLOG*
- **Blog toggle** — server config DEFAULT ON; OFF returns 404 and CLI reports disabled. — *BACKLOG*

### Phase 3 — Hardening & Coverage

- **Wasm and timing tests** — js-tagged client tests run under node (`make test-wasm`, `scripts/wasm_exec_runner.js`): wasm terminal line editing and the wasm proxy error path; production sleeps replaced by bounded waits (`sendWithRetry` waits up to 20ms for buffer space, `gracefulQuit` waits for pump exit with a 500ms cap). — *DONE*
- **Client config encryption** — guard limits load from the v3 envelope (`internal/config/config.go:15,447`, `client/config_other.go:70`); passphrase flows reused. — *DONE*
- **Blog docs** — `TECHNICAL.md` blog section + README EN+VI + E2E for the blog feature. — *BACKLOG*
- **Relay mesh** — a lightweight distribution network outside the server: each relay connects to one server plus many relays, and each client connects to one server plus many relays (CDN-style reads). Writes go to the server only (relays are read-only); relays share one wire protocol subset, alert each other with the client as the consumer, and serve as backup sources the client verifies against known chain tips. Not a federation: no cross-server identity or routing, one server's content only. — *NOT DONE*

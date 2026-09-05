# 🚀 V2V Anonymous WebSocket Chat
<p align="left">
🇻🇳
<a href="README.vi.md">Tiếng Việt</a> · <a href="docs/TECHNICAL.md">Technical Docs</a>
</p>

A lightweight, real-time anonymous chat with a tamper-evident message log — pure Go, WebSocket, no database.

- **Anonymous by default** — everyone gets `Name#a1b2` from a per-session salted IP hash (auto-extends, adds `-2` on collision). No signup, comfortable naming.
- **Tamper-evident log** — every message links into one server-wide hash chain and carries a `#height:hash` ID. Editing, reordering or renumbering any message breaks the chain, and every client sees it — no per-user signing needed for that.
- **Referenceable conversation** — quote replies (`/reply 1234`), `@#1234` mentions, and `/find` lookup by message number; code blocks render with syntax highlight.
- **Verifiable identity, optional** — a colorful `◆ ab12` tripcode badge derived from a passphrase, cryptographically verifiable (or log in passwordless as staff with Ed25519 key files / WebAuthn passkeys).
- **Fast & private** — capped in-memory history persisted as local JSONL, anti-spam limits, and encrypted key files stay on your device.

## Quick Start

**1. Get a binary**

Download from [releases](https://github.com/CleveTok3125/V2V/releases) or build:

```bash
make client          # -> public/V2V-linux-amd64 (host only)
make client ALL=1    # -> full matrix (7 platforms, for CI)
make dev             # -> bin/v2v, bin/v2v-server, bin/v2vctl + fresh webterm (dev build)
make help            # see all targets
```

**2. Join as guest**

```bash
./public/V2V-linux-amd64 -s wss://chat.example.com -u "YourName"
```

**3. Join with a tripcode**

```bash
./public/V2V-linux-amd64 -s wss://chat.example.com -u "YourName" -t "my secret phrase"
# you will appear as: YourName#ab12
#                      └─ ✍️ ◆ ab12cd34  (colored, clickable to verify)
```

Type `/help` inside the chat for commands (`/quit`, `/clear`, `/whoami`, `/status`, `/autoverify`, `/verify`, `/tab`, `/meta`, `/find`, `/reply`).

Your message first appears grey with `⏳` and is replaced by the confirmed line once the server echoes it back. Unknown `/commands` are rejected locally and never broadcast (to send text starting with `/`, wrap it in a ``` code block).

Chat and system messages live on separate tabs: `/tab` switches between Tab 1 (chat) and Tab 2 (local & system). The bar shows `[1:chat] 2:system` with the active tab in brackets.

Keys and settings live in your OS config dir (`~/.config/V2V/` on Linux, `%AppData%\V2V` on Windows, `~/Library/Application Support/V2V` on macOS): `key.json` for identities, auto-created `config.json` for settings. Override with `-c/--config-dir` and `-C/--cache-dir`.

## For Admins

Create identities with `v2vctl` (build with `make v2vctl` / `make v2vctl ALL=1` for all platforms):

```bash
# 1) Create the role (permissions live here, not in keygen)
./public/V2Vctl-linux-amd64 role create admin --unlimited --prefix "[Admin] "

# 2) Ed25519 key (classic, works everywhere)
./public/V2Vctl-linux-amd64 keygen ed25519 --role admin
# paste the printed snippet via: role add-identity admin --paste

# 3) Software passkey (WebAuthn wire format, for testing)
./public/V2Vctl-linux-amd64 keygen passkey --role admin --rpid chat.example.com --origin https://chat.example.com

# Login with a key file (-K path, or -k for the default key in the config dir)
./public/V2V-linux-amd64 -s wss://chat.example.com -u "Admin" -K key.json
```

Key files can be encrypted (`v2vctl` will ask for a passphrase, or use `V2V_PASSPHRASE`).

Web passkey enrollment (one-time link, 10 min):

```bash
./public/V2Vctl-linux-amd64 enroll --role member --label bob-laptop
# → https://chat.example.com/web/#enroll=...
```

See `template/.env` and `template/roles.json` for server configuration.

## Running the Server

**From source:**

```bash
cp template/.env .env          # edit PORT, ALLOWED_ORIGINS, etc.
make server web                # -> public/server.bin + webterm/app.wasm
./public/server.bin
# or: docker compose up -d --build   (persists ./data and ./logs)
# full matrix for release: make all ALL=1 -j4
```

Open `http://localhost:10000/web/` for the browser client.

## Learn More

- **How it works:** [`docs/TECHNICAL.md`](docs/TECHNICAL.md) — architecture, wire protocol, tripcode crypto, storage, and security model.
- **Configuration:** `template/.env` has all env vars with comments (`PORT`, `MAX_MESSAGE_LENGTH`, `HISTORY_FILE_PATH`, `WEBAUTHN_*`, etc.).
- **Management tool:** `v2vctl --help` and `v2vctl keygen --help`.

Issues and PRs are welcome.

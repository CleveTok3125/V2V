# 🚀 V2V — Verifiable Anonymous Chat
<p align="left">
🇻🇳
<a href="README.vi.md">Tiếng Việt</a> · <a href="docs/TECHNICAL.md">Technical Docs</a>
</p>

Chat without accounts. The server never asks for email, phone numbers, or any real identifying information.

- **No signup** — you join with a name like `Name#a1b2` and start chatting right away. No data links that name to you.
- **Optional identifier** — a tripcode badge (`◆ ab12`) generated from a passphrase lets others recognize you across sessions, as long as you keep using it.
- **Disposable identities** — just stop using a name or passphrase and that identity is gone. Starting over leaves no link between the old and the new.
- **Transparent history** — every message links into a single server-wide hash chain. If a message is edited or reordered, every client can detect the break.
- **Passwordless admin login** — moderators use Ed25519 key files or WebAuthn passkeys. No personal identifying information required.

## Why V2V Exists

Most chat platforms are built around accounts, profiles, and permanent identities. V2V explores a different model: **the conversation itself matters more than who is speaking**.

It fits situations where people don't need an account — joining with just a name — but the discussion still benefits from continuity: messages stay referenceable, and anyone can independently check that the history hasn't been altered.

That works for casual communities and for teams alike: any group that needs attributable, verifiable discussion without running account infrastructure.

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
# via proxy: --proxy socks5://127.0.0.1:1080 (http/https/socks5/socks5h;
# precedence --ask-proxy > --proxy > V2V_PROXY env > system proxy, or --ask-proxy for an interactive prompt)
```

**3. Join with a tripcode**

```bash
./public/V2V-linux-amd64 -s wss://chat.example.com -u "YourName" -t
# prompts for the secret tripcode (hidden input with a live strength
# meter, never passed as an argument); weak secrets ask to confirm
# (default No); offers to save it encrypted in tripcode.json afterwards.
# you will appear as: YourName#ab12
#                      └─ ✍️ ◆ ab12cd34  (colored, clickable to verify)
```
`V2V_TRIPCODE` env also works (CI only — prefer the encrypted file).

Type `/help` inside the chat for commands (`/quit`, `/clear`, `/clearhistory`, `/whoami`, `/status`, `/showjoin`, `/autoverify`, `/tab`, `/meta`, `/find`, `/reply`, `/info`, `/copy`).

Your message first appears grey with `⏳` and is replaced by the confirmed line once the server echoes it back. Unknown `/commands` are rejected locally and never broadcast (to send text starting with `/`, wrap it in a ``` code block).

Chat and system messages live on separate tabs: `/tab` switches between Tab 1 (chat) and Tab 2 (local & system). The bar shows `[1:chat] 2:system` with the active tab in brackets.

Keys and settings live in your OS config dir (`~/.config/V2V/` on Linux, `%AppData%\V2V` on Windows, `~/Library/Application Support/V2V` on macOS): `key.json` for identities, read-only `config.jsonc` for settings (JSONC comments allowed, copy `template/config.jsonc` to customize, `v2v --encrypt-config` to seal it). Override with `-c/--config-dir` (`V2V_CONFIG_DIR`) and `-C/--cache-dir` (`V2V_CACHE_DIR`). Extra flags: `-v` version, `-a` user-agent, `-i` server info, `-j` show join/leave (live display and catch-up history; replays filter joins by default).

## For Admins

Create identities with `v2vctl` (build with `make v2vctl` / `make v2vctl ALL=1` for all platforms):

```bash
# 1) Create the role (permissions live here, not in keygen)
./public/V2Vctl-linux-amd64 role create admin --unlimited --prefix "[Admin] "

# 2) Ed25519 key (classic, works everywhere)
./public/V2Vctl-linux-amd64 keygen ed25519 --role admin
# paste the printed snippet via: role add-identity admin --paste

# Login with a key file (-K path, or -k for the default key in the config dir)
./public/V2V-linux-amd64 -s wss://chat.example.com -u "Admin" -K key.json
```

Key files can be encrypted (`v2vctl` will ask for a passphrase, or use `V2V_PASSPHRASE`). Secrets travel as `[]byte` and are wiped from RAM after use.

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
- **Management tool:** `v2vctl --help` (`role create/list/show/update/delete/add-identity/import`, `keygen ed25519`, `enroll`, `migrate --preset native|wasm|custom`, `list`).

Issues and PRs are welcome.

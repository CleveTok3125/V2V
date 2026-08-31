# 🚀 V2V Anonymous WebSocket Chat
<p align="left">
🇻🇳
<a href="README.vi.md">Tiếng Việt</a> · <a href="docs/TECHNICAL.md">Technical Docs</a>
</p>

A lightweight, real-time anonymous chat — pure Go, WebSocket, no database.

- **Anonymous by default** — you get `Anonymous#ab12` from your IP hash. No signup.
- **Passwordless login for staff** — Ed25519 key files or WebAuthn passkeys (Touch ID / Windows Hello).
- **Optional tripcode** — a colorful `◆ ab12` badge derived from a passphrase, cryptographically verifiable.
- **Fast & private** — in-memory history, anti-spam limits, and encrypted key files stay on your device.

## Quick Start

**1. Get a binary**

Download from [releases](https://github.com/CleveTok3125/V2V/releases) or build:

```bash
make client          # -> public/V2V-linux-amd64 (or V2V-windows-amd64.exe)
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

Type `/help` inside the chat for commands (`/quit`, `/clear`, `/whoami`, `/status`, `/autoverify`).

## For Admins

Create identities with `v2vctl` (build with `make v2vctl`):

```bash
# 1) Ed25519 key (classic, works everywhere)
./public/V2Vctl-linux-amd64 keygen ed25519 --role admin --unlimited --prefix "[Admin] "

# 2) Software passkey (WebAuthn wire format, for testing)
./public/V2Vctl-linux-amd64 keygen passkey --role admin --rpid chat.example.com --origin https://chat.example.com

# Login with a key file
./public/V2V-linux-amd64 -s wss://chat.example.com -u "Admin" -k key.json
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
```

Open `http://localhost:10000/web/` for the browser client.

## Learn More

- **How it works:** [`docs/TECHNICAL.md`](docs/TECHNICAL.md) — architecture, wire protocol, tripcode crypto, storage, and security model.
- **Configuration:** `template/.env` has all env vars with comments (`PORT`, `MAX_MESSAGE_LENGTH`, `HISTORY_FILE_PATH`, `WEBAUTHN_*`, etc.).
- **Management tool:** `v2vctl --help` and `v2vctl keygen --help`.

Issues and PRs are welcome.

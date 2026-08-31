# 🚀 V2V Anonymous WebSocket Chat
<p align="left">
🇻🇳
<a href="README.vi.md">Tiếng Việt</a>
</p>
A high-performance, real-time anonymous chat system built entirely in Go. It features a lightweight WebSocket server and a native CLI (Terminal) client. The system incorporates robust security measures, including asymmetric cryptography for passwordless authentication, anti-spam mechanisms, and role-based permissions.

## Key Features

* **Lightning Fast and Lightweight:** Pure Go implementation utilizing `gorilla/websocket` for real-time bidirectional communication.
* **Asymmetric Authentication (Passwordless):** Uses Ed25519 and HMAC for a Challenge-Response authentication mechanism. This allows Admins/Mods to log in securely without sending private keys over the network, effectively preventing Replay and MITM attacks.
* **Real WebAuthn Passkeys (Web):** Members can log in from any browser using platform passkeys (Touch ID / Windows Hello / password managers) — private keys never leave the device. Enrollment links are issued by the server admin.
* **Secure Anonymity:** Users are anonymous by default. Display names are automatically appended with a short hash of the user's IP address (e.g., `Anonymous#1a2b`), making it easy to distinguish users without exposing real IP addresses. Optional tripcode provides a cryptographic pseudonym (`◆ ab12cd34`) derived from a passphrase bound to the server's identity.
* **Tripcode v0.7.0+ (ed25519 + hashchain):** Passphrase → Argon2id (`sha256(serverPub)[:16]` salt, `t=1/m=32MB` WASM / `t=3/m=64MB` native) → `ed25519` keypair; `badge = hex(sha256(pub))[:8]` colored via fixed palette. Each message is signed over `serverPub|seq|prev|msgHash|pub|displayName` with per-user hashchain `prev = sha256(prev|sig|msgHash)`, `seq` strictly increasing, `serverPub` binding prevents cross-server replay, `displayName` binding prevents fake `[Admin]` spoof, and `msgHash` is recomputed server- and client-side to detect history edits (history stores `WireMessage` JSON with `TripMeta` and is re-verified on load and on display, tampered messages show red badge).
* **Structured Wire & History:** Chat messages are `WireMessage` JSON (`type, time, displayName, text, trip`) rather than raw ANSI; `history.jsonl` stores `{"ts":"RFC3339Nano","wire":{...}}` (dedup, no top-level `trip`; system messages as `{"ts","msg"}`), the rotated `.old` is `zstd`-compressed to `.old.zst` (`50MB → ~3MB`, 2 generations max `~53MB`), and replay verifies trip integrity.
* **Anti-Spam and Abuse Protection:**
    * Maximum connection limits per IP address.
    * Message length and line-break limits.
    * Message and connection cooldowns.
    * Temporary IP lockouts for repeated failed authentication attempts.
    * IP spoofing and DoS preventation.
    * Immediately block unencrypted connections to prevent MITM attacks and secret sniffing

* **In-Memory Chat History:** `ChatHistory` is kept in RAM as deduped `WireMessage` JSON strings (evicted by `MaxHistoryBytes`, `cap>4*len` shrink) and streamed as `MaxHistorySend` messages to newcomers; `data/history.jsonl` is the durable store (`RFC3339Nano` `ts` readable, `zstd` `.old`, smart batch `Sync` every 1s only when dirty, `SIGTERM` drain, and `TripChains` repopulation on restart).
* **Cross-Platform CLI Client:** A terminal-based client featuring an integrated chat UI suitable for multi-line messages and local commands. Client auto-verifies trip signatures in a FIFO queue with parallel workers (enabled by default, `/autoverify` to toggle) and recolors badges locally; manual verification also available via `https://<host>/api/trip/verify?...` stateless endpoint (rate-limited 200ms/IP).
---

## Table of Contents

### 🤷‍♀️ Client
- [Installation](#installation)
- [Usage](#cli-client-usage)

### 🖥️ Server
- [Installation](#installation-1)
- [Configuration](#configurations)

---

## Client
### Installation

#### Option 1: using pre-built binaries

Compiled binaries for various platforms (Windows, Linux, macOS, Android) are available in [releases](https://github.com/CleveTok3125/V2V/releases).

Run the executable matching your operating system and architecture (e.g., `./V2V-linux-amd64` or `V2V-windows-amd64.exe`).

#### Option 2: building from source

**Build the Client:**

```bash
bash build_client.sh

```

---

### CLI Client Usage

The client runs directly in your terminal.

#### Basic Commands to Connect

**Join as a Guest:**

```bash
./client -s ws://localhost:8080 -u "YourName"

```

**Check Server Status:**

```bash
./client -s ws://localhost:8080 -i

```

**Join with a specific User-Agent:**

```bash
./client -s ws://localhost:8080 -u "YourName" -a "Custom-Agent/1.0"

```

#### Local Chat Commands
Once connected to a chat room,  you can type `/help` to see manual.

#### Authenticated Identities (key file & passkey)

Beyond guest access, the server accepts two privileged identity flavors —
both issued by an admin and stored locally in a single `key.json` container
(one ed25519 slot + one software-passkey slot):

| Flavor            | Created with                       | Login proof                          |
| ----------------- | ---------------------------------- | ------------------------------------ |
| `ed25519`         | `v2vctl keygen ed25519`         | Ed25519 signature over the handshake |
| Software passkey  | `v2vctl keygen passkey` (dev)   | ES256 assertion, WebAuthn wire format |

**Login:**

```bash
./client -s wss://chat.example.com -u "YourName" -k key.json
```

If the container holds both flavors you'll be asked which one to use.
Any authentication failure exits the client instead of degrading to a
guest session.

**Anti-phishing:** privileged identities are bound to the server's
Ed25519 identity — `key.json` stores `server_pubkey` (hex 64, from
`data/server_identity.json` auto-generated on first run) and signatures
cover `server_pubkey` instead of hostname. Real passkeys are pinned by RP
ID/origin — so an identity cannot be replayed against a different server
even behind a proxy.

**Key file encryption (v0.6.0+):** `key.json` can be encrypted at rest with
`XChaCha20Poly1305 + Argon2id` (simple but strong, works on WASM/arm64).
`v2vctl keygen` prompts `Passphrase (Enter = no encryption)` with hidden
input and confirmation; `V2V_PASSPHRASE` env or `--passphrase-file` also
supported. Encrypted files are `version:3` envelope with random salt/nonce
per file, `chmod 600`, and atomic `Sync` for durability.

**In-session commands:** `/whoami` shows your identity, role and
permissions; `/status` shows connection info and the client version; `/autoverify` toggles trip auto-verify (on by default).

#### Tripcode (passphrase → Ed25519, hashchain)

Tripcode is a per-user pseudonym independent from roles. Supply a passphrase via `-t` / `tripcode` field (web `type=password`):

```bash
./client -s wss://chat.example.com -u "YourName" -t "my secret phrase"
```

The client derives `ed25519` deterministically: `argon2id(passphrase, salt=sha256(serverPub)[:16]) → seed → ed25519`, `badge = hex(sha256(pub))[:8]` (e.g., `◆ ab12cd34`) colored from a fixed 10-color palette. No private key is sent; the passphrase is zeroed after derive.

Each chat message is signed as `sig = ed25519.Sign(priv, serverPub|seq|prev|msgHash|pub|displayName)` and sent as `{pub,seq,prev,sig,displayName}`. The server verifies `seq == last+1` and `prev` against `TripChains` (per-`pub` mutex), checks `msgHash == sha256(text)` and `ed25519.Verify` with `session.DisplayName` as ground truth (prevents `username="[Admin] Eve"` spoof), then stores `WireMessage` JSON in `history.jsonl` with `TripMeta` (`pub,seq,prev,sig,serverPub,msgHash,displayName`). On restart the server repopulates `TripChains` from history and re-verifies each record.

Clients auto-verify every trip message in a FIFO queue with parallel workers (local `ed25519.Verify`, not via the stateless `GET /api/trip/verify?pub&seq&prev&sig&msg_hash&server_pub&display_name` which is also available for manual click on the badge's `https` OSC8 link). Valid badges keep their palette color; tampered messages (edited `text` or `displayName` without updating `sig`) are shown in red `◆ ab12 ✗`.

**Web clients** log in through real platform passkeys (password-manager
popup). Enrollment is admin-issued: on the server host, run

```bash
./v2vctl enroll --role member --label bob-laptop --unlimited --prefix "[Member] "
```
If the `Role` field on the web login form receives `member:abc123…`, only `member` is kept (auto-parse `role:hash`).

then hand the printed one-time link (valid for 10 minutes) to the member,
who completes the popup ceremony from any browser.

Requires these environment variables on the server (see `.env.example`):

| Variable          | Example                      | Purpose                                    |
| ----------------- | ---------------------------- | ------------------------------------------ |
| `WEBAUTHN_RPID`   | `chat.example.com`          | Domain credentials are bound to            |
| `WEBAUTHN_ORIGIN` | `https://chat.example.com`  | Origin checked during every ceremony       |
| `WEBAUTHN_STORE`  | `./data/webauthn.json`       | Ticket + credential store (server-managed) |

---

## Server
### Installation 

#### Option 1: building from source
**Build the Server:**

```bash
bash build_server.sh

```

#### Option 2: Docker Deployment

Prerequisites: [Docker](https://docs.docker.com/get-docker/) installed.

1. **Prepare Configuration Files:**
   ensure you have your `.env` and `roles.json` files in the project root directory. You can create them if they don't exist:
   
   ```bash
   touch .env roles.json
    ```
2. **Build and Start the Server:**
run the following command in the project root directory. This will compile the Go binary inside the container and start the server in detached mode:

    ```bash
    docker compose up -d --build
    ```

3. **Check the Status:**
verify that the container is running:

    ```bash
    docker ps
    ```

    View Logs: to monitor real-time logs and see incoming connections:
    ```bash
    docker compose logs -f V2V
    ```

The docker-compose.yml mounts `./logs` and `./data` as directories (persisted), and `.env` as a file. `roles.json` is mounted as a **file** — due to Docker bind-mount inode caching, an atomic replacement (`keygen --merge-roles` does `Rename`) may not be visible inside the container until you restart:

```bash
docker compose restart v2v_server  # or down && up -d --build
```

`./logs` and `./data` (including `data/server_identity.json` auto-generated on first run, `data/history.jsonl` + `data/history.jsonl.old.zst` rotated at `MAX_HISTORY_FILE_SIZE_MB`, smart batch `Sync` every 1s only when dirty) survive restarts. By default, the template uses `LOG_FILE_PATH=./logs/app.log` and `HISTORY_FILE_PATH=./data/history.jsonl`.

For `.env` changes, the server hot-reloads dynamic vars automatically; `roles.json` and `server_identity.json` require a restart when replaced atomically.

- To stop and remove the container gracefully:

    ```bash
    docker compose down
    ```
- Restart the server:
    ```bash
    docker compose restart
    ```
### Configurations
#### Environment Variables

Before starting the server, you need to configure your environment variables:

1. The configuration templates are provided in `template/.env`.
2. Copy this file and paste it into the **project root directory** of the project, renaming it to `.env`.
3. Open the `.env` file and adjust the parameters to fit your setup (such as the server port, rate limits, allowed origins, etc). The server will automatically load these settings on startup.

#### Role-Based Authentication (Admins/Mods)

The system allows special privileges through cryptographic identities rather
than passwords. Identities are created with the **`v2vctl`** management
tool (build once from source: `go build -o v2vctl ./cmd/v2vctl`).

1. **Create an identity** — pick a flavor (server identity at `data/server_identity.json` is auto-generated on first run; its `public_key` is shown in logs as `Server public key`):

    ```bash
    # classic ed25519 key file (pin to current server's pubkey for anti-phishing)
    ./v2vctl keygen ed25519 --role admin --unlimited --prefix "[Admin] " --server-pubkey $(jq -r .public_key data/server_identity.json)

    # software passkey (WebAuthn wire format, signed natively at login)
    ./v2vctl keygen passkey --role admin \
        --rpid chat.example.com --origin https://chat.example.com
    ```

   Add `--passphrase` handling: `v2vctl` will prompt `Passphrase (Enter = no encryption)` with hidden confirm when run in a TTY, or read `V2V_PASSPHRASE` env in scripts. Encrypted `key.json` is `version:3`.

    *Both write the private material into `key.json` (keep it safe). The
    ed25519 flavor also prints a `roles.json` snippet; the passkey flavor can
    merge its public entry locally with `--merge-roles`.*

2. **Setup Server:** Place the generated `roles.json` file in the `./` directory on your server so it can verify your identity.
3. **Login:** Connect to the server using your private identity file:

    ```bash
    ./client -s ws://localhost:8080 -u "AdminName" -k /path/to/your/key.json

    ```

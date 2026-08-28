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
* **Secure Anonymity:** Users are anonymous by default. Display names are automatically appended with a short hash of the user's IP address (e.g., `Anonymous#1a2b`), making it easy to distinguish users without exposing real IP addresses. Now users can choose tripcode identity system.
* **Anti-Spam and Abuse Protection:**
    * Maximum connection limits per IP address.
    * Message length and line-break limits.
    * Message and connection cooldowns.
    * Temporary IP lockouts for repeated failed authentication attempts.
    * IP spoofing and DoS preventation.
    * Immediately block unencrypted connections to prevent MITM attacks and secret sniffing

* **In-Memory Chat History:** Automatically stores and sends the most recent messages to newly connected users.
* **Cross-Platform CLI Client:** A terminal-based client featuring an integrated chat UI suitable for multi-line messages and local commands.
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
| `ed25519`         | `V2V -g` → option 1                | Ed25519 signature over the handshake |
| Software passkey  | `V2V -g` → option 2 (dev bonus)    | ES256 assertion, WebAuthn wire format |

**Login:**

```bash
./client -s wss://chat.example.com -u "YourName" -k key.json
```

If the container holds both flavors you'll be asked which one to use.
Any authentication failure exits the client instead of degrading to a
guest session.

**Anti-phishing:** privileged identities are bound to the deployment
hostname — ed25519 signatures explicitly cover it, and real passkeys are
pinned by RP ID/origin — so an identity cannot be replayed against a
different server.

**In-session commands:** `/whoami` shows your identity, role and
permissions; `/status` shows connection info and the client version.

**Web clients** log in through real platform passkeys (password-manager
popup). Enrollment is admin-issued: on the server host, run

```bash
./v2v-admin enroll --role member --label bob-laptop --unlimited --prefix "[Member] "
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

The docker-compose.yml is configured to mount your local `.env` and `roles.json` directly into the running container as read-only files.
It also mounts `./logs` and `./data` so rotated logs and persisted chat history survive container restarts and recreates.
By default, the template uses `LOG_FILE_PATH=./logs/app.log` and `HISTORY_FILE_PATH=./data/history.jsonl`.

You do not need to restart the container when updating roles or environment variables.

Simply edit `.env` or `roles.json` on your host machine, save the file, and the server will _automatically_ detect the changes and reload the configurations on the fly.

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
than passwords. Identities are created with the **`v2v-admin`** management
tool (build once from source: `go build -o v2v-admin ./cmd/v2v-admin`).

1. **Create an identity** — pick a flavor:

    ```bash
    # classic ed25519 key file (signs the handshake nonce)
    ./v2v-admin keygen ed25519 --role admin --unlimited --prefix "[Admin] "

    # software passkey (WebAuthn wire format, signed natively at login)
    ./v2v-admin keygen passkey --role admin \
        --rpid chat.example.com --origin https://chat.example.com
    ```

    *Both write the private material into `key.json` (keep it safe). The
    ed25519 flavor also prints a `roles.json` snippet; the passkey flavor can
    merge its public entry locally with `--merge-roles`.*

2. **Setup Server:** Place the generated `roles.json` file in the `./` directory on your server so it can verify your identity.
3. **Login:** Connect to the server using your private identity file:

    ```bash
    ./client -s ws://localhost:8080 -u "AdminName" -k /path/to/your/key.json

    ```

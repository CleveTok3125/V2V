# 🚀 V2V Anonymous WebSocket Chat

A high-performance, real-time anonymous chat system built entirely in Go. It features a lightweight WebSocket server and a native CLI (Terminal) client. The system incorporates robust security measures, including asymmetric cryptography for passwordless authentication, anti-spam mechanisms, and role-based permissions.

## ✨ Key Features

* **Lightning Fast & Lightweight:** Pure Go implementation utilizing `gorilla/websocket` for real-time bidirectional communication.
* **Asymmetric Authentication (Passwordless):** Uses Ed25519 and HMAC for a Challenge-Response authentication mechanism. This allows Admins/Mods to log in securely without sending private keys over the network, effectively preventing Replay and MITM attacks.
* **Secure Anonymity:** Users are anonymous by default. Display names are automatically appended with a short hash of the user's IP address (e.g., `Anonymous#1a2b`), making it easy to distinguish users without exposing real IP addresses.
* **Anti-Spam & Abuse Protection:**
* Maximum connection limits per IP address.
* Message length and line-break limits.
* Message and connection cooldowns.
* Temporary IP lockouts for repeated failed authentication attempts.

* **In-Memory Chat History:** Automatically stores and sends the most recent messages to newly connected users.
* **Cross-Platform CLI Client:** A terminal-based client featuring an integrated chat UI and local commands.

---

## 🚀 Quick Start

You can either use the pre-built binaries or build the project from the source code.

### Option 1: Using Pre-built Binaries

Compiled binaries for various platforms (Windows, Linux, macOS, Android) are available in [Releases](https://github.com/CleveTok3125/V2V/releases).

Run the executable matching your operating system and architecture (e.g., `./V2V-linux-amd64` or `V2V-windows-amd64.exe`).

### Option 2: Building from Source

You can easily build the binaries using the provided shell scripts:

**Build the Server:**

```bash
bash build_server.sh

```

**Build the Client:**

```bash
bash build_client.sh

```

---

## 💻 CLI Client Usage

The client runs directly in your terminal.

### Basic Commands to Connect

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

### Local Chat Commands

Once connected to a chat room,  you can type `/help` to see manual.

---

## 💻 Server Usage

### ⚙️ Environment Configuration

Before starting the server, you need to configure your environment variables:

1. The configuration template provided in `template/.env`.
2. Copy this file and paste it into the **root directory** of the project or `/etc/secrets/`, renaming it to `.env`.
3. Open the `.env` file and adjust the parameters to fit your setup (such as the server port, rate limits, allowed origins, etc). The server will automatically load these settings on startup.

### 🔐 Role-Based Authentication (Admins/Mods)

The system allows special privileges through cryptographic keys rather than passwords.

1. **Generate Keys:** Run the client with the `-g` flag to generate a secure key pair.

```bash
./client -g

```

*This will generate `key.json` (Private Key - keep this safe) and `roles.json` (Public Key configuration).*
2. **Setup Server:** Place the generated `roles.json` file in the `./` or `/etc/secrets/` directory on your server so it can verify your identity.
3. **Login:** Connect to the server using your private key file:

```bash
./client -s ws://localhost:8080 -u "AdminName" -k /path/to/your/key.json

```

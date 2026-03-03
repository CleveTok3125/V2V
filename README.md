# V2V Chat — Anonymous WebSocket Chat System
<p align="right">
🌐
<a href="README.vi.md">Tiếng Việt</a>
</p>

# V2V Chat — Anonymous WebSocket Chat System
A lightweight, terminal-based anonymous chat system built with Go. Clients connect over WebSocket, are identified by a username and a 4-character IP hash suffix, and communicate in real time through a central server.

---

## ✨ Features

-   ⚡ Real-time messaging over WebSocket
-   🧑 Anonymous identity (username + IP hash)
-   🖥 Terminal-based client
-   🌐 Cross-platform binaries
-   🔒 Rate limiting & connection controls
-   📜 Configurable chat history
-   ☁️ Reverse proxy & Cloudflare ready
-   🐳 Docker deployment support

## 📦 Architecture Overview

    Client (Terminal App)
            │
            ▼
       WebSocket Connection
            │
            ▼
       V2V Chat Server (Go)
            │
            ├── Rate limiting
            ├── Message handling
            └── Chat history

## 📖 Table of Contents

- [System Requirements](#system-requirements)
- [Client Setup](#client-setup)
- [Server Setup](#server-setup)
  - [Environment Configuration](#environment-configuration)

---

## 🧰 System Requirements

### Client
- **OS:** macOS, Linux, Windows, or Android
- **Architecture:** `arm64` or `x64` 


### Server
- **Go** 1.25.4 or later (recommended)

---

## 🚀 Client Setup

1. Download the client from the [releases page](https://github.com/CleveTok3125/V2V/releases). Choose the build that matches your OS and architecture.

2. Assuming you downloaded it to the `Downloads` folder: (Change V2V to the name of the binary you downloaded)
   ```
   cd Downloads
   chmod +x V2V # Linux and macOS only
   ./V2V --help
   ```

3. Connect to the server:
   ```
   ./V2V -s <SERVER>
   # Example: ./V2V -s chat.example.com
   ```

---

## 🚀 Server Setup

### 1. Install Go

**macOS** (requires macOS 12 or later):
```bash
brew install go
go version   # verify the installation
```

**Linux:** Install Go using your distribution's package manager or from [go.dev/dl](https://go.dev/dl).

### 2. Install Dependencies

In the project directory, run:
```bash
go get github.com/gorilla/websocket
go get github.com/joho/godotenv
go mod tidy
```

---

### Environment Configuration

Create a file named `.env` (no other name — just `.env`) in the same directory as the server binary. Use the template below:

```env
# Connection & Rate Limiting
MAX_CONNECTIONS_PER_IP=<int>
CONNECTION_COOLDOWN=<int>s

# Messaging
MAX_MESSAGE_LENGTH=<int>
MAX_MESSAGE_LINE=<int>
MESSAGE_COOLDOWN=<int>ms

# Chat History
MAX_HISTORY_BYTES=<int>
MAX_HISTORY_SEND=<int>

# Identity
MAX_USERNAME_LENGTH=<int>

# UI & Display
STATUS_URL=<URL str>
DOWNLOAD_URL=<URL str>
HOMEPAGE_URL=<URL str>
INSTANCE_ID=<i dont know>
TIMEZONE=Asia/Ho_Chi_Minh
```

> **Note:** `TIMEZONE` accepts any [IANA timezone name](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones) (e.g. `America/New_York`, `UTC`). If omitted or invalid, the server falls back to the system local time. All other variables are required — the server will exit on startup if any are missing.

---



# V2V Chat — Anonymous WebSocket Chat System

A command line-oriented anonymous chat system for Vietnamese users

---

## ✨ Features

- ⚡ Real-time messaging over WebSocket
- 🧑 Anonymous identity (username + IP hash)
- 🖥 Terminal-based client
- 🌐 Cross-platform binaries
- 🔒 Rate limiting & connection controls
- 📜 Configurable chat history
- ☁️ Reverse proxy & Cloudflare ready
- 🐳 Docker deployment support

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

1. Visit the homepage at [https://chat.elsutm.io.vn](https://chat.elsutm.io.vn) and find the **Install Client** line.

2. Download the client from the [releases page](https://github.com/CleveTok3125/V2V/releases). Choose the build that matches your OS and architecture.

3. Open your favourite terminal. On macOS/Linux use Terminal; on Windows use Command Prompt. Example:  navigate to your download folder:

   ```
   cd Downloads
   ```

4. Make the file executable (macOS and Linux only):

   ```
   chmod +x V2V-*
   ```

5. Connect to the server:

   ```
   ./V2V-* -s <SERVER>
   # Example: ./V2V-* -s chat.elsutm.io.vn
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

Create a file named `.env` in the same directory as the server binary or in `/etc/secrets/`. Use the template below:

```env
# Connection & Rate Limiting
MAX_CONNECTIONS_PER_IP=1
CONNECTION_COOLDOWN=1s

# Messaging
MAX_MESSAGE_LENGTH=1000
MAX_MESSAGE_LINE=10
MESSAGE_COOLDOWN=200ms

# Chat History
MAX_HISTORY_BYTES=10485760
MAX_HISTORY_SEND=50

# Identity
MAX_USERNAME_LENGTH=12

# UI & Display
STATUS_URL=https://example.com/status
DOWNLOAD_URL=https://example.com/download
HOMEPAGE_URL=https://example.com/
INSTANCE_ID=your-server-instance-id
TIMEZONE=Asia/Ho_Chi_Minh
```

> **Note:** `TIMEZONE` accepts any [IANA timezone name](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones) (e.g. `America/New_York`, `UTC`). If omitted or invalid, the server falls back to the system local time. All other variables are required — the server will exit on startup if any are missing.

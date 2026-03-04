# V2V Chat — Hệ thống Nhắn tin Ẩn danh dùng WebSocket  
<p align="right">
🌐
<a href="README.md">English</a> 
</p>

Một hệ thống nhắn tin gọn nhẹ, ẩn danh dựa dùng terminal được phát triển bằng Go. Client kết nối qua WebSocket, được nhận diện bằng username và hash IP suffix 4 kí tự và liên lạc theo thời gian thực thông qua một máy chủ trung gian. 


## ✨ Các tính năng nổi bật

-   ⚡ Nhắn tin theo thời gian thực qua WebSocket
-   🧑 Danh tính ẩn danh (username + hash IP)
-   🖥 Máy khách dùng Terminal
-   🌐 Tệp cài đặt đa nền tảng
-   🔒 Giới hạn tốc độ & kiểm soát kết nối
-   📜 Lịch sử tin nhắn cấu hình được
-   ☁️ Proxy ngược và có sẵn Cloudflare
-   🐳 Hỗ trợ triển khai trong Docker

## 📦 Tổng quan Kiến trúc

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

## 📖 Mục lục

- [Yêu cầu hệ thống](#yêu-cầu-hệ-thống)
- [Thiết lập máy khách](#thiết-lập-máy-khách)
- [Thiết lập máy chủ](#thiết-lập-máy-chủ)
  - [Cấu hình môi trường](#environment-configuration)

---

## 🧰 Yêu cầu hệ thống

### Client
- **HĐH:** macOS, Linux, Windows, hoặc Android
- **Kiến trúc:** `arm64` hoặc `x64` 


### Server
- **Go** 1.25.4 hoặc mới hơn (khuyến nghị)
- **SSL/TLS** cho trải nghiệm nhắn tin an toàn (khuyến nghị)

---

## 🚀 Thiết lập Client

1. Tải client từ trang [releases page](https://github.com/CleveTok3125/V2V/releases). Chọn phiên bản phù hợp với HĐH và kiến trúc của bạn.

2. Giả sử bạn tải về thư mục Tải xuống (Downloads), đổi tên tệp cài đặt thành V2V:
   ```
   cd Downloads
   chmod +x V2V # đổi mode để chạy, chỉ Linux và macOS 
   ./V2V --help
   ```

3. Kết nối với Server:
   ```
   ./V2V -s <SERVER>
   # Example: ./V2V -s https://chat.elsutm.io.vn
   ```

---

## 🚀 Thiết lập Server

### 1. Cài đặt Go

**macOS** (yêu cầu macOS 12 hoặc mới hơn):
```bash
brew install go
go version   # kiểm tra xem cài được chưa
```

**Linux:** Cài đặt Go từ trình quản lí gói với [go.dev/dl](https://go.dev/dl).

### 2. Cài đặt các dependencies

Trong đường dẫn của project, chạy các lệnh sau:
```bash
go get github.com/gorilla/websocket
go get github.com/joho/godotenv
go mod tidy
```

---

### Cấu hình môi trường

Tạo một tệp tên `.env` (không có gì cả, chỉ `.env`) trong cùng thư mục với tệp server. Dùng ngay mẫu dưới đây:

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

> **Chú ý:** `TIMEZONE` nhận toàn bộ [múi giờ IANA](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones) (e.g. `America/New_York`, `UTC`). Nếu bỏ qua hoặc không hợp lệ, server tự chuyển về giờ địa phương của nó. Tất cả các biến đều được yêu cầu. Server sẽ thoát lúc khởi chạy nếu bất cứ thứ gì bị thiếu. 

---



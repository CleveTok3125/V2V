# 🚀 V2V Anonymous WebSocket Chat
<p align="left">
🌐
<a href="README.md">English</a>
</p>
Hệ thống chat ẩn danh WebSocket thời gian thực, hiệu năng cao, được xây dựng hoàn toàn bằng Go. Bao gồm một WebSocket server nhẹ và một client CLI (Terminal) thuần túy. Hệ thống tích hợp các biện pháp bảo mật mạnh mẽ như mã hóa bất đối xứng để xác thực không cần mật khẩu, cơ chế chống spam, và phân quyền theo vai trò.

## Các tính năng nổi bật

* **Nhanh và nhẹ:** Triển khai thuần Go sử dụng `gorilla/websocket` cho giao tiếp hai chiều thời gian thực.
* **Xác thực bất đối xứng (không cần mật khẩu):** Sử dụng Ed25519 và HMAC theo cơ chế Challenge-Response. Cho phép Admin/Mod đăng nhập an toàn mà không cần truyền private key qua mạng, ngăn chặn hiệu quả các tấn công Replay và MITM. 
* **WebAuthn Passkey thật (Web):** Thành viên đăng nhập từ bất kỳ trình duyệt nào bằng platform passkey (Touch ID / Windows Hello / password manager) — private key không bao giờ rời thiết bị. Link enroll do admin phát.
* **Ẩn danh an toàn:** Người dùng mặc định là ẩn danh. Tên hiển thị tự động được gắn thêm một đoạn hash ngắn từ địa chỉ IP (ví dụ: `Anonymous#1a2b`), giúp phân biệt người dùng mà không lộ IP thật. Tripcode là pseudonym mật mã (`◆ ab12cd34`) suy ra từ passphrase và gắn với `server_pubkey` của server.
* **Tripcode v0.7.0+ (ed25519 + hashchain):** Passphrase → `Argon2id` (`salt=sha256(serverPub)[:16]`, `t=1/m=32MB` WASM / `t=3/m=64MB` native) → `ed25519`; `badge = hex(sha256(pub))[:8]` tô màu qua palette 10 màu cố định. Mỗi tin nhắn được ký `serverPub|seq|prev|msgHash|pub|displayName` với chuỗi `prev = sha256(prev|sig|msgHash)`, `seq` tăng nghiêm ngặt, `serverPub` chống replay cross-server, `displayName` chống mạo danh `[Admin]`, và `msgHash` được tính lại cả phía server và client để phát hiện sửa `history.jsonl` (tin giả hiện badge đỏ).
* **Wire có cấu trúc & lịch sử:** `WireMessage` JSON (`type,time,displayName,text,trip`); `history.jsonl` lưu `{"ts":"RFC3339Nano","wire":{...}}` (dedup, không trùng `trip` top-level, system là `{"ts","msg"}`), file `.old` được `zstd` nén thành `.old.zst` (`50MB → ~3MB`, 2 gen `~53MB` max), replay verify lại trip.
* **Chống spam và lạm dụng:**
    * Giới hạn số lượng kết nối tối đa theo địa chỉ IP.
    * Giới hạn độ dài tin nhắn và số dòng.
    * Cooldown cho tin nhắn và kết nối.
    * Tạm khóa IP khi xác thực thất bại nhiều lần liên tiếp.
    * Chống IP spoofing và DoS.
    * Tạm thời chặn kết nối không mã hoá để ngăn tấn công MITM và nghe lén
* **Lịch sử chat trong bộ nhớ:** `ChatHistory` giữ `WireMessage` JSON dedup (evict theo `MaxHistoryBytes`, shrink `cap>4*len`), gửi `MaxHistorySend` tin cho người mới; `data/history.jsonl` bền vững (`RFC3339Nano` readable, batch `Sync` 1s chỉ khi dirty, `SIGTERM` drain, repopulate `TripChains` khi restart).
* **Client CLI đa nền tảng:** Client chạy trên terminal với giao diện chat tích hợp. Client tự động verify trip trong hàng đợi FIFO với worker song song (mặc định bật, `/autoverify` để tắt/mở) và tô màu lại badge; click vào badge mở `https://<host>/api/trip/verify?...` stateless (giới hạn 200ms/IP).

---
## 📖 Mục lục

### 🤷‍♀️ Client
- [Cài đặt](#cài-đặt)
- [Sử dụng](#hướng-dẫn-sử-dụng-cli-client)

### 🖥️ Server
- [Cài đặt](#cài-đặt-server)
- [Cấu hình](#cấu-hình)

---

## Client
### Cài đặt

#### Cách 1: dùng binary dựng sẵn

Các binary đã biên dịch cho nhiều nền tảng (Windows, Linux, macOS, Android) có sẵn tại [releases](https://github.com/CleveTok3125/V2V/releases).

Chạy file thực thi phù hợp với hệ điều hành và kiến trúc của bạn (ví dụ: `./V2V-linux-amd64` hoặc `V2V-windows-amd64.exe`).

#### Cách 2: tự build từ mã nguồn

Bạn có thể build dễ dàng bằng các script có sẵn:

**Build Client:**
```bash
bash build_client.sh
```

---

### Hướng dẫn sử dụng CLI Client

Client chạy trực tiếp trong terminal của bạn.

#### Các lệnh kết nối cơ bản

**Tham gia với tư cách khách:**
```bash
./client -s ws://localhost:8080 -u "TênCủaBạn"
```

**Kiểm tra trạng thái server:**
```bash
./client -s ws://localhost:8080 -i
```

**Tham gia với User-Agent tùy chỉnh:**
```bash
./client -s ws://localhost:8080 -u "TênCủaBạn" -a "Custom-Agent/1.0"
```

#### Lệnh trong phòng chat

Sau khi kết nối, gõ `/help` để xem hướng dẫn sử dụng.

#### Danh tính có xác thực (key file & passkey)

Ngoài truy cập khách, server chấp nhận hai loại danh tính đặc quyền — cả hai
được lưu trong một container `key.json` (1 slot ed25519 + 1 slot passkey mềm):

| Loại           | Tạo bằng                         | Chứng minh đăng nhập                        |
| -------------- | -------------------------------- | ------------------------------------------- |
| `ed25519`      | `v2v-admin keygen ed25519`       | Chữ ký Ed25519 lên handshake                |
| Passkey mềm    | `v2v-admin keygen passkey` (dev) | Assertion ES256 theo wire format WebAuthn   |

**Đăng nhập:**

```bash
./client -s wss://chat.example.com -u "TênCủaBạn" -k key.json
```

Nếu container có cả 2 slot, client sẽ hỏi chọn. Bất kỳ lỗi xác thực nào cũng
kết thúc phiên thay vì rơi xuống khách.

**Chống phishing:** mọi danh tính đặc quyền gắn với `server_pubkey` Ed25519 của server — `key.json` lưu `server_pubkey` (hex 64, từ `data/server_identity.json` tự sinh lần đầu) và chữ ký bao phủ `server_pubkey` thay vì hostname. Passkey thật được pin bằng RP ID/origin.

**Mã hoá key file (v0.6.0+):** `key.json` có thể mã hoá at-rest bằng `XChaCha20Poly1305 + Argon2id` (nhẹ nhưng mạnh, chạy được WASM/arm64). `v2v-admin keygen` sẽ hỏi `Passphrase (Enter = không mã hoá)` ẩn khi chạy TTY hoặc đọc `V2V_PASSPHRASE`.

**Lệnh trong phiên:** `/whoami` xem danh tính + quyền; `/status` xem kết nối + phiên bản; `/autoverify` bật/tắt tự verify trip (mặc định bật).

#### Tripcode (passphrase → Ed25519, hashchain)

Tripcode là pseudonym riêng cho mỗi user, độc lập với role. Nhập passphrase qua `-t` / ô `tripcode` (`type=password`):

```bash
./client -s wss://chat.example.com -u "TênCủaBạn" -t "cụm bí mật của bạn"
```

Client suy ra `ed25519` xác định: `argon2id(passphrase, salt=sha256(serverPub)[:16]) → seed → ed25519`, `badge = hex(sha256(pub))[:8]` (ví dụ `◆ ab12cd34`) tô màu qua palette 10 màu cố định. Không gửi private key; passphrase được `zero` sau khi suy ra.

Mỗi tin chat được ký `sig = ed25519.Sign(priv, serverPub|seq|prev|msgHash|pub|displayName)` và gửi `{pub,seq,prev,sig,displayName}`. Server verify `seq == last+1` và `prev` với `TripChains` (khoá theo `pub`), kiểm tra `msgHash == sha256(text)` và `ed25519.Verify` với `session.DisplayName` làm ground truth (chống `username="[Admin] Eve"`), rồi lưu `WireMessage` JSON vào `history.jsonl` kèm `TripMeta`. Khi restart, server repopulate `TripChains` từ history và kiểm tra lại từng record.

Client tự verify mọi tin trip trong hàng đợi FIFO với worker song song (local `ed25519.Verify`, không qua API để tránh rate-limit `200ms/IP`), tint lại badge: hợp lệ giữ màu palette, giả (sửa `text`/`displayName` mà không cập nhật `sig`) hiện đỏ `◆ ab12 ✗`. Click vào badge (link `https://<host>/api/trip/verify?pub&seq&prev&sig&msg_hash&server_pub&display_name`) cũng cho phép verify thủ công qua API stateless.

**Web** đăng nhập bằng **passkey thật** (popup password manager). Kênh cấp:
admin phát link one-time trên host server:

```bash
./v2v-admin enroll --role member --label bob-laptop
```

Member mở link trong 10 phút bằng bất kỳ browser nào để hoàn tất popup ceremony.

Yêu cầu biến môi trường phía server (xem `.env.example` / `template/.env`):

| Biến              | Ví dụ                       | Công dụng                              |
| ----------------- | --------------------------- | -------------------------------------- |
| `WEBAUTHN_RPID`   | `chat.example.com`         | Domain gắn với credential              |
| `WEBAUTHN_ORIGIN` | `https://chat.example.com` | Origin kiểm tra ở mỗi lần ceremony     |
| `WEBAUTHN_STORE`  | `./data/webauthn.json`      | Nơi lưu ticket + credential (server)   |

---

## Server
### Cài đặt Server

#### Cách 1: tự build từ mã nguồn

**Build Server:**
```bash
bash build_server.sh
```

#### Cách 2: dùng Docker 

Yêu cầu: [Docker](https://docs.docker.com/get-docker/) đã được cài đặt.

1. **Chuẩn bị file cấu hình:**
   đảm bảo rằng bạn có file `.env` và `roles.json` trong thư mục gốc của project. Bạn có thể tạo chúng nếu chưa có:

   ```bash
   touch .env roles.json
    ```
2. **Build và khởi động Server:**
chạy các lệnh sau đây trong thư mục gốc của project. Nó sẽ biên dịch mã Go bên trong container và khởi động server ở chế độ nền

    ```bash
    docker compose up -d --build
    ```

3. **Kiểm tra trạng thái:**

    ```bash
    docker ps
    ```

    Xem Nhật kí: giám sát nhật kí theo thời gian thực và thấy các kết nối đến
    ```bash
    docker compose logs -f V2V
    ```

docker-compose.yml được cấu hình để mount trực tiếp `.env` và `roles.json` vào container đang chạy dưới dạng chỉ đọc.
Nó cũng mount `./logs` và `./data`, nên log xoay vòng và chat history được lưu xuống disk sẽ còn sau khi restart hoặc recreate container.
Mặc định template dùng `LOG_FILE_PATH=./logs/app.log` và `HISTORY_FILE_PATH=./data/history.jsonl`.

Bạn không cần khởi động lại container khi cập nhật roles hoặc biến môi trường.

Chỉ cần chỉnh sửa file `.env` hoặc `roles.json` trên máy chủ, lưu lại file, và máy chủ sẽ *tự động* phát hiện các thay đổi và tải lại các cấu hình ngay lập tức.

- Dừng và xoá container một cách an toàn:

    ```bash
    docker compose down
    ```
- Khởi động lại máy chủ:
    ```bash
    docker compose restart
    ```

### Cấu hình
#### Biến môi trường

Trước khi khởi động server, bạn cần thiết lập các biến môi trường:

1. Template cấu hình có sẵn tại `template/.env`.
2. Sao chép file này vào **thư mục gốc** của project, đổi tên thành `.env`.
3. Mở file `.env` và điều chỉnh các thông số cho phù hợp (cổng server, giới hạn rate, allowed origins, v.v.). Server sẽ tự động tải các cài đặt này khi khởi động.

#### Xác thực theo vai trò (Admin/Mod)

Hệ thống cấp quyền đặc biệt thông qua khóa mã hóa thay vì mật khẩu.

1. **Tạo cặp khóa:** Chạy client với flag `-g` để tạo cặp khóa bảo mật.
    ```bash
    ./v2v-admin keygen
    ```
    *Lệnh này sẽ tạo ra `key.json` (Private Key — hãy giữ cẩn thận) và `roles.json` (cấu hình Public Key).*

2. **Cài đặt trên server:** Đặt file `roles.json` vào thư mục `./` trên server để server có thể xác minh danh tính của bạn.

3. **Đăng nhập:** Kết nối tới server bằng file private key:
    ```bash
    ./client -s ws://localhost:8080 -u "TênAdmin" -k /đường/dẫn/tới/key.json
    ```

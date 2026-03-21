# 🚀 V2V Anonymous WebSocket Chat
<p align="left">
🌐
<a href="README.md">English</a>
</p>
Hệ thống chat ẩn danh WebSocket thời gian thực, hiệu năng cao, được xây dựng hoàn toàn bằng Go. Bao gồm một WebSocket server nhẹ và một client CLI (Terminal) thuần túy. Hệ thống tích hợp các biện pháp bảo mật mạnh mẽ như mã hóa bất đối xứng để xác thực không cần mật khẩu, cơ chế chống spam, và phân quyền theo vai trò.

## Các tính năng nổi bật

* **Nhanh và nhẹ:** Triển khai thuần Go sử dụng `gorilla/websocket` cho giao tiếp hai chiều thời gian thực.
* **Xác thực bất đối xứng (không cần mật khẩu):** Sử dụng Ed25519 và HMAC theo cơ chế Challenge-Response. Cho phép Admin/Mod đăng nhập an toàn mà không cần truyền private key qua mạng, ngăn chặn hiệu quả các tấn công Replay và MITM. 
* **Ẩn danh an toàn:** Người dùng mặc định là ẩn danh. Tên hiển thị tự động được gắn thêm một đoạn hash ngắn từ địa chỉ IP (ví dụ: `Anonymous#1a2b`), giúp phân biệt người dùng mà không lộ IP thật. Bây giờ người dùng có thêm quyền lựa chọn hệ thống xác thực tripcode.
* **Chống spam và lạm dụng:**
    * Giới hạn số lượng kết nối tối đa theo địa chỉ IP.
    * Giới hạn độ dài tin nhắn và số dòng.
    * Cooldown cho tin nhắn và kết nối.
    * Tạm khóa IP khi xác thực thất bại nhiều lần liên tiếp.
    * Chống IP spoofing và DoS.
    * Tạm thời chặn kết nối không mã hoá để ngăn tấn công MITM và nghe lén
* **Lịch sử chat trong bộ nhớ:** Tự động lưu và gửi các tin nhắn gần nhất cho người dùng mới kết nối.
* **Client CLI đa nền tảng:** Client chạy trên terminal với giao diện chat tích hợp và các lệnh cục bộ.

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

docker-compose.yml được cấu hình để mount file .env và roles.json trực tiếp vào container đang chạy. Mặc định, file Docker mount ở thư mục logs. Vì vậy, bạn phải đổi đường dẫn của nhật kí để trỏ đến vị trí trong thư mục logs.

Bạn không cần khởi động lại container khi cập nhật roles hoặc biến môi trường.

Chỉ cần chỉnh sửa file .env hoặc roles.json trên máy chủ, lưu lại file, và máy chủ sẽ *tự động* phát hiện các thay đổi và tải lại các cấu hình ngay lập tức.

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
    ./client -g
    ```
    *Lệnh này sẽ tạo ra `key.json` (Private Key — hãy giữ cẩn thận) và `roles.json` (cấu hình Public Key).*

2. **Cài đặt trên server:** Đặt file `roles.json` vào thư mục `./` trên server để server có thể xác minh danh tính của bạn.

3. **Đăng nhập:** Kết nối tới server bằng file private key:
    ```bash
    ./client -s ws://localhost:8080 -u "TênAdmin" -k /đường/dẫn/tới/key.json
    ```

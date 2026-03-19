# 🚀 V2V Anonymous WebSocket Chat
<p align="left">
🌐
<a href="README.md">English</a>
</p>
Hệ thống chat ẩn danh WebSocket thời gian thực, hiệu năng cao, được xây dựng hoàn toàn bằng Go. Bao gồm một WebSocket server nhẹ và một client CLI (Terminal) thuần túy. Hệ thống tích hợp các biện pháp bảo mật mạnh mẽ như mã hóa bất đối xứng để xác thực không cần mật khẩu, cơ chế chống spam, và phân quyền theo vai trò.

## ✨ Tính năng nổi bật

* **Nhanh & Nhẹ:** Triển khai thuần Go sử dụng `gorilla/websocket` cho giao tiếp hai chiều thời gian thực.
* **Xác thực bất đối xứng (không cần mật khẩu):** Sử dụng Ed25519 và HMAC theo cơ chế Challenge-Response. Cho phép Admin/Mod đăng nhập an toàn mà không cần truyền private key qua mạng, ngăn chặn hiệu quả các tấn công Replay và MITM. 
* **Ẩn danh an toàn:** Người dùng mặc định là ẩn danh. Tên hiển thị tự động được gắn thêm một đoạn hash ngắn từ địa chỉ IP (ví dụ: `Anonymous#1a2b`), giúp phân biệt người dùng mà không lộ IP thật. Bây giờ người dùng có thêm quyền lựa chọn hệ thống xác thực tripcode.
* **Chống spam & lạm dụng:**
    * Giới hạn số lượng kết nối tối đa theo địa chỉ IP.
    * Giới hạn độ dài tin nhắn và số dòng.
    * Cooldown cho tin nhắn và kết nối.
    * Tạm khóa IP khi xác thực thất bại nhiều lần liên tiếp.
    * Chống IP spoofing và DoS.
    * Tạm thời chặn kết nối không mã hoá để ngăn tấn công MITM và nghe lén
* **Lịch sử chat trên bộ nhớ:** Tự động lưu và gửi các tin nhắn gần nhất cho người dùng mới kết nối.
* **Client CLI đa nền tảng:** Client chạy trên terminal với giao diện chat tích hợp và các lệnh cục bộ.

---

## 🚀 Bắt đầu nhanh

Bạn có thể dùng các file binary dựng sẵn hoặc tự build từ mã nguồn.

### Lựa chọn 1: Dùng binary dựng sẵn

Các binary đã biên dịch cho nhiều nền tảng (Windows, Linux, macOS, Android) có sẵn tại [releases](https://github.com/CleveTok3125/V2V/releases).

Chạy file thực thi phù hợp với hệ điều hành và kiến trúc của bạn (ví dụ: `./V2V-linux-amd64` hoặc `V2V-windows-amd64.exe`).

### Lựa chọn 2: Tự build từ mã nguồn

Bạn có thể build dễ dàng bằng các script có sẵn:

**Build Server:**
```bash
bash build_server.sh
```

**Build Client:**
```bash
bash build_client.sh
```

---

## 💻 Hướng dẫn sử dụng CLI Client

Client chạy trực tiếp trong terminal của bạn.

### Các lệnh kết nối cơ bản

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

### Lệnh trong phòng chat

Sau khi kết nối, gõ `/help` để xem hướng dẫn sử dụng.

---

## 💻 Vận hành Server

### ⚙️ Cấu hình môi trường

Trước khi khởi động server, bạn cần thiết lập các biến môi trường:

1. Template cấu hình có sẵn tại `template/.env`.
2. Sao chép file này vào **thư mục gốc** của project hoặc `/etc/secrets/`, đổi tên thành `.env`.
3. Mở file `.env` và điều chỉnh các thông số cho phù hợp (cổng server, giới hạn rate, allowed origins, v.v.). Server sẽ tự động tải các cài đặt này khi khởi động.

### 🔐 Xác thực theo vai trò (Admin/Mod)

Hệ thống cấp quyền đặc biệt thông qua khóa mã hóa thay vì mật khẩu.

1. **Tạo cặp khóa:** Chạy client với flag `-g` để tạo cặp khóa bảo mật.
    ```bash
    ./client -g
    ```
    *Lệnh này sẽ tạo ra `key.json` (Private Key — hãy giữ cẩn thận) và `roles.json` (cấu hình Public Key).*

2. **Cài đặt trên server:** Đặt file `roles.json` vào thư mục `./` hoặc `/etc/secrets/` trên server để server có thể xác minh danh tính của bạn.

3. **Đăng nhập:** Kết nối tới server bằng file private key:
    ```bash
    ./client -s ws://localhost:8080 -u "TênAdmin" -k /đường/dẫn/tới/key.json
    ```

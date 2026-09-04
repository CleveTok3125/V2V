# 🚀 V2V Anonymous WebSocket Chat
<p align="left">
🌐
<a href="README.md">English</a> · <a href="docs/TECHNICAL.md">Tài liệu kỹ thuật</a>
</p>

Chat ẩn danh, nhẹ, chạy hoàn toàn bằng Go — không cần database.

- **Ẩn danh mặc định** — mọi người, kể cả admin, đều có `Tên#a1b2` từ IP hash với salt ngẫu nhiên mỗi phiên (tự mở rộng, thêm `-2` khi trùng). Không cần đăng ký, đặt tên thoải mái.
- **Đăng nhập không mật khẩu** — key Ed25519 hoặc passkey WebAuthn (Touch ID / Windows Hello).
- **Tripcode tùy chọn** — badge `◆ ab12` từ passphrase, có thể xác thực bằng mật mã.
- **Nhanh & riêng tư** — lịch sử trong RAM + file, chống spam, key được mã hóa trên máy bạn.

## Bắt đầu nhanh

**1. Lấy binary**

Tải từ [releases](https://github.com/CleveTok3125/V2V/releases) hoặc tự build:

```bash
make client          # -> public/V2V-linux-amd64 (chỉ host)
make client ALL=1    # -> full matrix 7 nền tảng (dành cho CI)
make dev             # -> bin/v2v, bin/v2v-server, bin/v2vctl + webterm mới (bản dev)
make help            # xem tất cả target
```

**2. Vào phòng như khách**

```bash
./public/V2V-linux-amd64 -s wss://chat.example.com -u "TênBạn"
```

**3. Dùng tripcode**

```bash
./public/V2V-linux-amd64 -s wss://chat.example.com -u "TênBạn" -t "cụm bí mật của bạn"
# bạn sẽ hiện: TênBạn#ab12
#               └─ ✍️ ◆ ab12cd34  (màu, bấm để verify)
```

Gõ `/help` trong phòng để xem lệnh (`/quit`, `/clear`, `/whoami`, `/status`, `/autoverify`, `/verify`, `/tab`).

Tin nhắn của bạn hiện xám kèm `⏳` trước, rồi được thay bằng dòng xác nhận khi server gửi lại (echo). Lệnh `/` lạ (không dấu cách) bị chặn ngay trên máy, không gửi đi; muốn gửi chữ bắt đầu bằng `/` thì thêm dấu cách sau slash (`/ hello`).

Chat và system nằm ở 2 tab riêng: `/tab` chuyển giữa Tab 1 (chat) và Tab 2 (local & system). Thanh tab hiện `[1:chat] 2:system`, tab đang xem nằm trong ngoặc.

Key và cấu hình nằm trong thư mục config của hệ điều hành (`~/.config/V2V/` trên Linux, `%AppData%\V2V` trên Windows, `~/Library/Application Support/V2V` trên macOS): `key.json` cho danh tính, `config.json` tự tạo cho cài đặt. Ghi đè bằng `-c/--config-dir` và `-C/--cache-dir`.

## Dành cho Admin

Tạo danh tính bằng `v2vctl` (`make v2vctl` / `make v2vctl ALL=1` cho full matrix):

```bash
# Tạo role trước (quyền nằm ở role, không nằm ở keygen)
./public/V2Vctl-linux-amd64 role create admin --unlimited --prefix "[Admin] "

# Ed25519 classic
./public/V2Vctl-linux-amd64 keygen ed25519 --role admin
# dán snippet in ra bằng: role add-identity admin --paste

# Passkey mềm (dev)
./public/V2Vctl-linux-amd64 keygen passkey --role admin --rpid chat.example.com --origin https://chat.example.com

# Đăng nhập bằng key (-K đường dẫn, hoặc -k để dùng key mặc định trong config dir)
./public/V2V-linux-amd64 -s wss://chat.example.com -u "Admin" -K key.json
```

Key có thể mã hóa (`v2vctl` sẽ hỏi passphrase, hoặc dùng `V2V_PASSPHRASE`).

Cấp passkey web (link dùng 1 lần, 10 phút):

```bash
./public/V2Vctl-linux-amd64 enroll --role member --label bob-laptop
# → https://chat.example.com/web/#enroll=...
```

Xem `template/.env` và `template/roles.json` để cấu hình server.

## Chạy Server

**Từ mã nguồn:**

```bash
cp template/.env .env          # sửa PORT, ALLOWED_ORIGINS, ...
make server web                # -> public/server.bin + webterm/app.wasm
./public/server.bin
# hoặc: docker compose up -d --build   (lưu ./data và ./logs)
# full matrix: make all ALL=1 -j4
```

Mở `http://localhost:10000/web/` cho bản web.

## Tìm hiểu thêm

- **Chi tiết kỹ thuật:** [`docs/TECHNICAL.md`](docs/TECHNICAL.md) — kiến trúc, giao thức wire, tripcode, lưu trữ, bảo mật.
- **Cấu hình:** `template/.env` có đủ biến môi trường với comment.
- **Công cụ quản trị:** `v2vctl --help` và `v2vctl keygen --help`.

Báo lỗi và PR luôn được chào đón.

# 🚀 V2V — Verifiable Anonymous Chat
<p align="left">
🌐
<a href="README.md">English</a> · <a href="docs/TECHNICAL.md">Tài liệu kỹ thuật</a>
</p>

Trò chuyện không cần tài khoản. Máy chủ không bao giờ yêu cầu email, số điện thoại hay bất kỳ thông tin định danh thực nào.

- **Không cần đăng ký** — bạn tham gia với tên dạng `Name#a1b2` và bắt đầu trò chuyện ngay. Không có dữ liệu nào liên kết tên đó với bạn.
- **Tên định danh tùy chọn** — mã định danh tripcode (`◆ ab12`) tạo từ cụm mật khẩu giúp người khác nhận ra bạn qua các phiên trò chuyện, miễn là bạn vẫn tiếp tục sử dụng nó.
- **Danh tính dùng một lần** — chỉ cần ngừng sử dụng tên hoặc cụm mật khẩu, danh tính đó sẽ biến mất. Khi bắt đầu lại, sẽ không có mối liên kết nào giữa danh tính cũ và mới.
- **Lịch sử minh bạch** — mọi tin nhắn đều được liên kết vào một chuỗi băm (hash chain) duy nhất trên toàn hệ thống máy chủ. Nếu tin nhắn bị chỉnh sửa hoặc thay đổi thứ tự, mọi ứng dụng khách (client) đều có thể phát hiện sự đứt gãy của chuỗi này.
- **Đăng nhập cho quản trị viên không cần mật khẩu** — người điều hành sử dụng tệp khóa Ed25519 hoặc khóa truy cập WebAuthn (passkey). Không yêu cầu thông tin định danh cá nhân.

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
# qua proxy: --proxy socks5://127.0.0.1:1080 (http/https/socks5/socks5h;
# thứ tự --ask-proxy > --proxy > env V2V_PROXY > proxy hệ thống, hoặc --ask-proxy để nhập bằng prompt)
```

**3. Dùng tripcode**

```bash
./public/V2V-linux-amd64 -s wss://chat.example.com -u "TênBạn" -t
# prompt nhập tripcode bí mật (che khi gõ, có thước đo độ mạnh realtime,
# không truyền qua argument); secret yếu hỏi xác nhận (mặc định Không);
# hỏi lưu mã hóa vào tripcode.json sau đó.
# bạn sẽ hiện: TênBạn#ab12
#               └─ ✍️ ◆ ab12cd34  (màu, bấm để verify)
```
Biến môi trường `V2V_TRIPCODE` cũng dùng được (chỉ cho CI — nên dùng file mã hóa).

Gõ `/help` trong phòng để xem lệnh (`/quit`, `/clear`, `/clearhistory`, `/whoami`, `/status`, `/showjoin`, `/autoverify`, `/tab`, `/meta`, `/find`, `/reply`, `/info`, `/copy`).

Tin nhắn của bạn hiện xám kèm `⏳` trước, rồi được thay bằng dòng xác nhận khi server gửi lại (echo). Lệnh `/` lạ bị chặn ngay trên máy, không gửi đi (muốn gửi chữ bắt đầu bằng `/` thì bọc trong codeblock ```).

Chat và system nằm ở 2 tab riêng: `/tab` chuyển giữa Tab 1 (chat) và Tab 2 (local & system). Thanh tab hiện `[1:chat] 2:system`, tab đang xem nằm trong ngoặc.

Key và cấu hình nằm trong thư mục config của hệ điều hành (`~/.config/V2V/` trên Linux, `%AppData%\V2V` trên Windows, `~/Library/Application Support/V2V` trên macOS): `key.json` cho danh tính, `config.json` tự tạo cho cài đặt. Ghi đè bằng `-c/--config-dir` (`V2V_CONFIG_DIR`) và `-C/--cache-dir` (`V2V_CACHE_DIR`). Flag phụ: `-v` phiên bản, `-a` user-agent, `-i` thông tin server, `-j` hiện ra/vào.

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

Key có thể mã hóa (`v2vctl` sẽ hỏi passphrase, hoặc dùng `V2V_PASSPHRASE`). Secret đi qua `[]byte` và bị xóa khỏi RAM sau khi dùng.

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
- **Công cụ quản trị:** `v2vctl --help` (`role create/list/show/update/delete/add-identity/add-passkey/import`, `keygen ed25519|passkey`, `enroll`, `migrate --preset native|wasm|custom`, `list`).

Báo lỗi và PR luôn được chào đón.

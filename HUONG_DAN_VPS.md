# Triển khai zPanel (Phong cách One-click)

Để triển khai đơn giản như zPanel (chỉ với 1 dòng lệnh), bạn có thể làm theo cách sau:

## 1. Chuẩn bị (Trên máy tính cá nhân - Windows)
Nếu bạn đang dùng Windows, bạn có 2 cách để biên dịch cho Linux:

**Cách 1: Sử dụng PowerShell (Khuyên dùng)**
1. Mở PowerShell trong thư mục dự án.
2. Chạy file: `./build.ps1`
   - File thực thi cho Linux sẽ nằm tại `build/linux/zpanel`.

**Cách 2: Sử dụng Makefile (Nếu đã cài GNU Make)**
- Chạy lệnh: `make linux`

## 2. Cách triển khai One-Click (Nếu bạn đã tải lên bảng điều khiển)

Sau khi bạn đã nén thư mục chứa `zpanel` và `install.sh` thành `zpanel.tar.gz` và để nó trên một máy chủ web (ví dụ: GitHub hoặc server riêng), người dùng chỉ cần chạy:

```bash
# Đối với Ubuntu
wget -O install.sh http://your-domain.com/install.sh && sudo bash install.sh

# Đối với CentOS
curl -sSO http://your-domain.com/install.sh && bash install.sh
```

## 2. Kịch bản cài đặt tinh gọn (`install.sh`)

Tôi đã tối ưu hóa `install.sh` để:
- Tự động nhận diện hệ điều hành (Ubuntu/CentOS).
- Tự động cài đặt mọi dependencies.
- Tự động mở port trên Firewall (UFW/FirewallD).
- Tự động tạo dịch vụ chạy ngầm (Systemd).

## 3. Quy trình đề xuất cho bạn (Chủ sở hữu bảng điều khiển)

Để có trải nghiệm "giống zPanel nhất", hãy làm như sau:
1. **Biên dịch**: `make linux`
2. **Đóng gói**: Nén file `zpanel` và `install.sh`.
3. **Upload**: Đưa lên một host công khai.
4. **Chia sẻ**: Gửi lệnh wget/curl cho người dùng cuối.

 ---
**Lệnh cài đặt mẫu (Bạn có thể copy và sửa URL):**
```bash
URL="http://your-server.com/zpanel"
wget -O zpanel $URL && chmod +x zpanel && ./zpanel -install
```

*(Lưu ý: Tôi đã thiết kế bảng điều khiển này theo hướng "Lite" - tức là không cần cài đặt phức tạp, chỉ cần duy nhất 1 file thực thi là có thể chạy ngay lập tức).*

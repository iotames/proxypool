package node

import (
	"net"
	"testing"
)

func TestEncodeSocks5AddrIPv4(t *testing.T) {
	addr := "1.2.3.4:80"
	buf := EncodeSocks5Addr(addr)

	if len(buf) != 7 { // type(1) + ip(4) + port(2)
		t.Fatalf("IPv4 地址长度应为 7, 实际 %d", len(buf))
	}
	if buf[0] != 0x01 {
		t.Fatalf("地址类型应为 0x01, 实际 0x%02x", buf[0])
	}
	if buf[1] != 1 || buf[2] != 2 || buf[3] != 3 || buf[4] != 4 {
		t.Fatalf("IP 地址编码错误: %v", buf[1:5])
	}
	port := int(buf[5])<<8 | int(buf[6])
	if port != 80 {
		t.Fatalf("端口编码错误, 期望 80, 实际 %d", port)
	}
}

func TestEncodeSocks5AddrDomain(t *testing.T) {
	addr := "www.example.com:443"
	buf := EncodeSocks5Addr(addr)

	if buf[0] != 0x03 {
		t.Fatalf("域名地址类型应为 0x03, 实际 0x%02x", buf[0])
	}
	domainLen := int(buf[1])
	if domainLen != len("www.example.com") {
		t.Fatalf("域名长度错误, 期望 %d, 实际 %d", len("www.example.com"), domainLen)
	}
	domain := string(buf[2 : 2+domainLen])
	if domain != "www.example.com" {
		t.Fatalf("域名编码错误, 期望 'www.example.com', 实际 '%s'", domain)
	}
	port := int(buf[2+domainLen])<<8 | int(buf[2+domainLen+1])
	if port != 443 {
		t.Fatalf("端口编码错误, 期望 443, 实际 %d", port)
	}
}

func TestEncodeSocks5AddrIPv6(t *testing.T) {
	addr := "[::1]:8080"
	buf := EncodeSocks5Addr(addr)

	if buf[0] != 0x04 {
		t.Fatalf("IPv6 地址类型应为 0x04, 实际 0x%02x", buf[0])
	}
	if len(buf) != 19 { // type(1) + ip(16) + port(2)
		t.Fatalf("IPv6 地址长度应为 19, 实际 %d", len(buf))
	}
	ip := net.IP(buf[1:17])
	if !ip.Equal(net.ParseIP("::1")) {
		t.Fatalf("IPv6 地址错误, 期望 ::1, 实际 %s", ip.String())
	}
	port := int(buf[17])<<8 | int(buf[18])
	if port != 8080 {
		t.Fatalf("端口编码错误, 期望 8080, 实际 %d", port)
	}
}

func TestEncodeSocks5AddrInvalid(t *testing.T) {
	// 无效地址
	if buf := EncodeSocks5Addr("invalid"); buf != nil {
		t.Fatal("无效地址应返回 nil")
	}
}

func TestEncodeSocks5AddrLongDomain(t *testing.T) {
	// 超长域名
	longHost := ""
	for i := 0; i < 300; i++ {
		longHost += "a"
	}
	addr := net.JoinHostPort(longHost, "443")
	buf := EncodeSocks5Addr(addr)

	if buf[0] != 0x03 {
		t.Fatalf("域名类型应为 0x03")
	}
	if int(buf[1]) > 255 {
		t.Fatalf("域名长度不能超过 255, 实际 %d", buf[1])
	}
}

func TestDecodeSocks5AddrIPv4(t *testing.T) {
	encoded := []byte{0x01, 192, 168, 1, 1, 0x01, 0xBB} // 192.168.1.1:443
	host, port := DecodeSocks5Addr(encoded)
	if host != "192.168.1.1" {
		t.Fatalf("主机解析错误, 期望 192.168.1.1, 实际 %s", host)
	}
	if port != 443 {
		t.Fatalf("端口解析错误, 期望 443, 实际 %d", port)
	}
}

func TestDecodeSocks5AddrDomain(t *testing.T) {
	domain := "google.com"
	encoded := []byte{0x03, byte(len(domain))}
	encoded = append(encoded, []byte(domain)...)
	encoded = append(encoded, 0x00, 0x50) // port 80

	host, port := DecodeSocks5Addr(encoded)
	if host != "google.com" {
		t.Fatalf("主机解析错误, 期望 google.com, 实际 %s", host)
	}
	if port != 80 {
		t.Fatalf("端口解析错误, 期望 80, 实际 %d", port)
	}
}

func TestDecodeSocks5AddrInvalid(t *testing.T) {
	// 太短的数据
	host, port := DecodeSocks5Addr([]byte{0x01})
	if host != "" || port != 0 {
		t.Fatal("太短的数据应返回空结果")
	}

	// 未知地址类型
	host, port = DecodeSocks5Addr([]byte{0xFF, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	if host != "" || port != 0 {
		t.Fatal("未知地址类型应返回空结果")
	}
}

func TestRoundTrip(t *testing.T) {
	tests := []string{
		"1.2.3.4:80",
		"8.8.8.8:53",
		"www.google.com:443",
		"example.com:8080",
		"[::1]:3000",
	}

	for _, addr := range tests {
		encoded := EncodeSocks5Addr(addr)
		if encoded == nil {
			t.Fatalf("编码失败: %s", addr)
		}
		host, port := DecodeSocks5Addr(encoded)
		decoded := net.JoinHostPort(host, itoa(port))
		// 注意: IPv6 地址编码后可能与原始格式不同（去掉方括号）
		if addr != decoded && "["+decoded+"]" != addr {
			// IPv4 和域名应该精确匹配
			t.Errorf("编解码往返失败: 原始=%s, 解码=%s (encoded=%x)", addr, decoded, encoded)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

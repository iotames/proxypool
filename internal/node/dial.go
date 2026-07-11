package node

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"time"
)

// DialThroughNode 通过指定的代理节点连接到目标地址。
// targetAddr 格式为 "host:port"。
// 返回的 net.Conn 已经完成协议握手，可直接传输数据。
func DialThroughNode(n *Node, targetAddr string, timeout int) (net.Conn, error) {
	switch n.Type {
	case "trojan":
		return dialTrojan(n, targetAddr, timeout)
	case "ss":
		return dialSS(n, targetAddr, timeout)
	default:
		return nil, fmt.Errorf("不支持的节点类型: %s", n.Type)
	}
}

// dialTrojan 通过 trojan-go 协议连接到目标地址。
//
// Trojan-Go 协议格式（Clash Verge 使用此实现）：
//  1. TLS 连接到服务器（SNI 使用配置中的 sni 字段）
//  2. 发送 [SHA224(password)]\r\n
//  3. 发送 [CMD=0x01][SOCKS5 地址格式]\r\n
//  4. 后续数据直接转发
func dialTrojan(n *Node, targetAddr string, timeout int) (net.Conn, error) {
	// 1. TCP 连接
	conn, err := net.DialTimeout("tcp", n.Addr(), time.Duration(timeout)*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("连接 trojan 服务器(%s)失败: %w", n.Addr(), err)
	}

	// 设置初始超时完成握手
	_ = conn.SetDeadline(time.Now().Add(time.Duration(timeout) * time.Millisecond))

	// 2. TLS 包装（trojan-go 使用 TLS 传输）
	sni := n.SNI
	if sni == "" {
		sni = n.Server
	}
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: n.SkipCertVerify,
		NextProtos:         []string{"h2", "http/1.1"},
	})
	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("trojan TLS 握手失败: %w", err)
	}

	// 3. 发送 SHA224 哈希密码 + CRLF
	passwordHash := sha224Hex(n.Password)
	if _, err := tlsConn.Write([]byte(passwordHash + "\r\n")); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("发送 trojan 密码哈希失败: %w", err)
	}

	// 4. 发送 CMD(0x01) + SOCKS5 地址 + CRLF
	addrBytes := EncodeSocks5Addr(targetAddr)
	request := append([]byte{0x01}, addrBytes...)
	request = append(request, []byte("\r\n")...)
	if _, err := tlsConn.Write(request); err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("发送 trojan 目标地址失败: %w", err)
	}

	// 清除 deadline，后续由调用方管理
	_ = conn.SetDeadline(time.Time{})
	_ = tlsConn.SetDeadline(time.Time{})

	return tlsConn, nil
}

// sha224Hex 计算字符串的 SHA224 哈希并返回十六进制字符串。
func sha224Hex(s string) string {
	h := sha256.New224()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// EncodeSocks5Addr 将 "host:port" 格式的地址编码为 SOCKS5 地址格式。
func EncodeSocks5Addr(addr string) []byte {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	var portInt int
	fmt.Sscanf(portStr, "%d", &portInt)
	port := make([]byte, 2)
	port[0] = byte(portInt >> 8)
	port[1] = byte(portInt & 0xFF)
	ip := net.ParseIP(host)
	if ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			buf := make([]byte, 1+4+2)
			buf[0] = 0x01
			copy(buf[1:], ip4)
			buf[5], buf[6] = port[0], port[1]
			return buf
		}
		buf := make([]byte, 1+16+2)
		buf[0] = 0x04
		copy(buf[1:], ip.To16())
		buf[17], buf[18] = port[0], port[1]
		return buf
	}
	domainLen := len(host)
	if domainLen > 255 {
		domainLen = 255
	}
	buf := make([]byte, 1+1+domainLen+2)
	buf[0] = 0x03
	buf[1] = byte(domainLen)
	copy(buf[2:], host[:domainLen])
	buf[2+domainLen], buf[2+domainLen+1] = port[0], port[1]
	return buf
}

// DecodeSocks5Addr 从 SOCKS5 地址格式中解析出 "host:port" 字符串。
func DecodeSocks5Addr(data []byte) (string, int) {
	if len(data) < 3 {
		return "", 0
	}
	addrType := data[0]
	var host string
	var addrLen int
	switch addrType {
	case 0x01:
		if len(data) < 7 {
			return "", 0
		}
		host = net.IP(data[1:5]).String()
		addrLen = 4
	case 0x03:
		if len(data) < 2 {
			return "", 0
		}
		domainLen := int(data[1])
		if len(data) < 2+domainLen+2 {
			return "", 0
		}
		host = string(data[2 : 2+domainLen])
		addrLen = 1 + domainLen
	case 0x04:
		if len(data) < 17 {
			return "", 0
		}
		host = net.IP(data[1:17]).String()
		addrLen = 16
	default:
		return "", 0
	}
	portOffset := 1 + addrLen
	if len(data) < portOffset+2 {
		return "", 0
	}
	port := int(data[portOffset])<<8 | int(data[portOffset+1])
	return host, port
}

// dialSS 通过 shadowsocks AEAD 协议连接到目标地址。
func dialSS(n *Node, targetAddr string, timeout int) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", n.Addr(), time.Duration(timeout)*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("连接 ss 服务器(%s)失败: %w", n.Addr(), err)
	}
	aeadConn, err := newSSConn(conn, n.Cipher, n.Password, targetAddr)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("建立 ss 加密连接失败: %w", err)
	}
	return aeadConn, nil
}

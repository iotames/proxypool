package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"strings"

	"github.com/iotames/proxypool/internal/node"
)

// maxHTTPHeaderBytes 限制 HTTP 请求头总大小，防止畸形请求耗尽内存。
const maxHTTPHeaderBytes = 64 << 10

// errHeaderTooLarge 请求头超过大小上限。
var errHeaderTooLarge = errors.New("请求头超过大小限制")

// httpRequestHeader 代理请求头解析结果。
type httpRequestHeader struct {
	Method  string
	Target  string
	Version string
	Fields  [][2]string // 待转发给目标服务器的请求头（已剔除代理专用头）
	Session string      // Proxy-Authorization 解析出的粘性会话键，空表示不使用会话
}

// readHTTPRequestHeader 读取并解析完整的 HTTP 请求头（直到空行）。
// Proxy-Authorization 与 Proxy-Connection 属于代理层头部，解析后不再转发给目标服务器。
func readHTTPRequestHeader(br *bufio.Reader) (*httpRequestHeader, error) {
	total := 0
	line, err := readHeaderLine(br, &total)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("无效的请求行: %q", line)
	}

	h := &httpRequestHeader{Method: parts[0], Target: parts[1], Version: parts[2]}
	for {
		line, err := readHeaderLine(br, &total)
		if err != nil {
			return nil, err
		}
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)

		switch strings.ToLower(name) {
		case "proxy-authorization":
			if h.Session == "" {
				h.Session = sessionKeyFromAuth(value)
			}
			continue
		case "proxy-connection":
			continue
		}
		h.Fields = append(h.Fields, [2]string{name, value})
	}
	return h, nil
}

// readHeaderLine 读取一行请求头并累计已读字节数。
func readHeaderLine(br *bufio.Reader, total *int) (string, error) {
	line, err := br.ReadString('\n')
	*total += len(line)
	if *total > maxHTTPHeaderBytes {
		return "", errHeaderTooLarge
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// Header 返回指定名称的请求头值（不区分大小写）。
func (h *httpRequestHeader) Header(name string) string {
	for _, f := range h.Fields {
		if strings.EqualFold(f[0], name) {
			return f[1]
		}
	}
	return ""
}

// handleCONNECT 处理 HTTP CONNECT 隧道请求。
//
// 客户端请求格式：
//
//	CONNECT host:port HTTP/1.1\r\n
//	Host: host:port\r\n
//	\r\n
//
// 服务器回复：
//
//	HTTP/1.1 200 Connection Established\r\n
//	\r\n
func handleCONNECT(clientConn net.Conn, targetAddr string, targetNode *node.Node, timeout int) {
	// 检查是否为有效的 host:port
	if _, _, err := net.SplitHostPort(targetAddr); err != nil {
		log.Printf("无效的 CONNECT 目标地址: %s\n", targetAddr)
		sendHTTPError(clientConn, 400, "Bad Request")
		return
	}

	// 通过节点连接到目标
	remoteConn, err := node.DialThroughNode(targetNode, targetAddr, timeout)
	if err != nil {
		log.Printf("通过节点(%s)连接目标(%s)失败: %v\n", targetNode.Name, targetAddr, err)
		sendHTTPError(clientConn, 502, "Bad Gateway")
		return
	}
	defer remoteConn.Close()

	// 回复 200 Connection Established
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		log.Printf("发送 CONNECT 响应失败: %v\n", err)
		return
	}

	// 双向转发数据
	log.Printf("[CONNECT] %s -> %s (via %s)\n", clientConn.RemoteAddr(), targetAddr, targetNode.Name)
	relay(clientConn, remoteConn)
}

// handleHTTP 处理普通 HTTP 代理请求。
//
// 客户端请求格式：
//
//	GET http://host:port/path HTTP/1.1\r\n
//	Host: host:port\r\n
//	\r\n
func handleHTTP(clientConn net.Conn, br *bufio.Reader, hdr *httpRequestHeader, targetNode *node.Node, timeout int) {
	// 解析目标 URL；兼容 origin-form（仅路径）请求，此时依赖 Host 头
	u, err := url.Parse(hdr.Target)
	if err != nil {
		log.Printf("解析 URL 失败: %s, %v\n", hdr.Target, err)
		sendHTTPError(clientConn, 400, "Bad Request")
		return
	}
	if u.Host == "" {
		u.Host = hdr.Header("Host")
		if u.Host == "" {
			log.Printf("请求缺少目标主机: %s\n", hdr.Target)
			sendHTTPError(clientConn, 400, "Bad Request")
			return
		}
	}

	targetHost := u.Host
	if _, _, err := net.SplitHostPort(targetHost); err != nil {
		if u.Scheme == "https" {
			targetHost = net.JoinHostPort(targetHost, "443")
		} else {
			targetHost = net.JoinHostPort(targetHost, "80")
		}
	}

	// 通过节点连接到目标
	remoteConn, err := node.DialThroughNode(targetNode, targetHost, timeout)
	if err != nil {
		log.Printf("通过节点(%s)连接目标(%s)失败: %v\n", targetNode.Name, targetHost, err)
		sendHTTPError(clientConn, 502, "Bad Gateway")
		return
	}
	defer remoteConn.Close()

	// 构建转发请求（将绝对路径转为相对路径）
	var forwardReq strings.Builder
	forwardReq.WriteString(fmt.Sprintf("%s %s %s\r\n", hdr.Method, u.RequestURI(), hdr.Version))
	for _, f := range hdr.Fields {
		forwardReq.WriteString(f[0])
		forwardReq.WriteString(": ")
		forwardReq.WriteString(f[1])
		forwardReq.WriteString("\r\n")
	}
	forwardReq.WriteString("\r\n")

	// 发送请求到目标
	if _, err := remoteConn.Write([]byte(forwardReq.String())); err != nil {
		log.Printf("发送请求到目标失败: %v\n", err)
		return
	}

	// 转发已被 bufio 读入缓冲区的请求体部分
	if n := br.Buffered(); n > 0 {
		body := make([]byte, n)
		if _, err := io.ReadFull(br, body); err == nil {
			if _, err := remoteConn.Write(body); err != nil {
				log.Printf("发送请求体到目标失败: %v\n", err)
				return
			}
		}
	}

	log.Printf("[HTTP] %s %s (via %s)\n", hdr.Method, targetHost, targetNode.Name)

	// 双向转发
	relay(clientConn, remoteConn)
}

// sendHTTPError 向客户端发送 HTTP 错误响应。
func sendHTTPError(conn net.Conn, statusCode int, message string) {
	response := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		statusCode, httpStatusText(statusCode), len(message), message)
	conn.Write([]byte(response))
}

// httpStatusText 返回 HTTP 状态码对应的文本描述。
func httpStatusText(code int) string {
	switch code {
	case 200:
		return "OK"
	case 400:
		return "Bad Request"
	case 405:
		return "Method Not Allowed"
	case 502:
		return "Bad Gateway"
	case 503:
		return "Service Unavailable"
	default:
		return "Unknown"
	}
}

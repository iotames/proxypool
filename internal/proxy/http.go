package proxy

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"strings"

	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
)

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
func handleCONNECT(clientConn net.Conn, initialBuf []byte, p *pool.Pool, timeout int) {
	// 从初始缓冲区读取完整的 CONNECT 请求
	reader := bufio.NewReader(io.MultiReader(
		strings.NewReader(string(initialBuf)),
		clientConn,
	))

	// 读取请求行
	requestLine, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("读取 CONNECT 请求行失败: %v\n", err)
		return
	}
	requestLine = strings.TrimSpace(requestLine)

	// 从 requestLine 中提取 host:port
	parts := strings.Split(requestLine, " ")
	if len(parts) >= 2 {
		targetAddr := parts[1]
		// 检查是否为有效的 host:port
		if _, _, err := net.SplitHostPort(targetAddr); err != nil {
			log.Printf("无效的 CONNECT 目标地址: %s\n", targetAddr)
			sendHTTPError(clientConn, 400, "Bad Request")
			return
		}

		// 读取并丢弃剩余的请求头
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			if strings.TrimSpace(line) == "" {
				break
			}
		}

		// 从池中随机选取节点
		targetNode := p.GetRandom()
		if targetNode == nil {
			log.Println("代理池为空，无法处理 CONNECT 请求")
			sendHTTPError(clientConn, 503, "No Available Proxy")
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
		_, err = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		if err != nil {
			log.Printf("发送 CONNECT 响应失败: %v\n", err)
			return
		}

		// 双向转发数据
		log.Printf("[CONNECT] %s -> %s (via %s)\n", clientConn.RemoteAddr(), targetAddr, targetNode.Name)
		relay(clientConn, remoteConn)
	}
}

// handleHTTP 处理普通 HTTP 代理请求。
//
// 客户端请求格式：
//
//	GET http://host:port/path HTTP/1.1\r\n
//	Host: host:port\r\n
//	\r\n
func handleHTTP(clientConn net.Conn, initialBuf []byte, p *pool.Pool, timeout int) {
	reader := bufio.NewReader(io.MultiReader(
		strings.NewReader(string(initialBuf)),
		clientConn,
	))

	// 读取请求行
	requestLine, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("读取 HTTP 请求行失败: %v\n", err)
		return
	}
	requestLine = strings.TrimSpace(requestLine)

	// 解析请求行: METHOD URL HTTP/VERSION
	parts := strings.SplitN(requestLine, " ", 3)
	if len(parts) < 3 {
		log.Printf("无效的 HTTP 请求行: %s\n", requestLine)
		return
	}

	method := parts[0]
	requestURL := parts[1]

	// 解析目标 URL
	u, err := url.Parse(requestURL)
	if err != nil {
		log.Printf("解析 URL 失败: %s, %v\n", requestURL, err)
		return
	}

	targetHost := u.Host
	if _, _, err := net.SplitHostPort(targetHost); err != nil {
		// 没有端口，添加默认端口
		if u.Scheme == "https" {
			targetHost = net.JoinHostPort(targetHost, "443")
		} else {
			targetHost = net.JoinHostPort(targetHost, "80")
		}
	}

	// 从池中选取节点
	targetNode := p.GetRandom()
	if targetNode == nil {
		log.Println("代理池为空，无法处理 HTTP 请求")
		sendHTTPError(clientConn, 503, "No Available Proxy")
		return
	}

	// 通过节点连接到目标
	remoteConn, err := node.DialThroughNode(targetNode, targetHost, timeout)
	if err != nil {
		log.Printf("通过节点(%s)连接目标(%s)失败: %v\n", targetNode.Name, targetHost, err)
		sendHTTPError(clientConn, 502, "Bad Gateway")
		return
	}
	defer remoteConn.Close()

	// 读取剩余的请求头
	var headers strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		headers.WriteString(line)
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	// 构建转发请求（将绝对路径转为相对路径）
	var forwardReq strings.Builder
	forwardReq.WriteString(fmt.Sprintf("%s %s HTTP/1.1\r\n", method, u.RequestURI()))
	forwardReq.WriteString(headers.String())

	// 发送请求到目标
	if _, err := remoteConn.Write([]byte(forwardReq.String())); err != nil {
		log.Printf("发送请求到目标失败: %v\n", err)
		return
	}

	// 如果有请求体，转发请求体
	if reader.Buffered() > 0 {
		body := make([]byte, reader.Buffered())
		reader.Read(body)
		remoteConn.Write(body)
	}

	log.Printf("[HTTP] %s %s (via %s)\n", method, targetHost, targetNode.Name)

	// 双向转发
	relay(clientConn, remoteConn)
}

// sendHTTPError 向客户端发送 HTTP 错误响应。
func sendHTTPError(conn net.Conn, statusCode int, message string) {
	response := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Length: %d\r\n\r\n%s",
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
	case 502:
		return "Bad Gateway"
	case 503:
		return "Service Unavailable"
	default:
		return "Unknown"
	}
}

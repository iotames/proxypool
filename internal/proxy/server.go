// Package proxy 实现隧道代理服务。
// 支持 HTTP CONNECT、普通 HTTP 代理和 SOCKS5 三种协议的自动识别与转发。
// 每个新连接都会从代理池中随机选取一个节点进行转发。
package proxy

import (
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/iotames/proxypool/internal/pool"
)

// Server 隧道代理服务器。
type Server struct {
	address  string      // 监听地址，格式为 "host:port"
	pool     *pool.Pool  // 代理池
	timeout  int         // 连接超时（毫秒）
	listener net.Listener
}

// NewServer 创建一个新的代理服务器实例。
// addr: 监听地址，如 "127.0.0.1:1080"。
// p: 代理池实例。
// timeout: 节点连接超时（毫秒）。
func NewServer(addr string, p *pool.Pool, timeout int) *Server {
	return &Server{
		address: addr,
		pool:    p,
		timeout: timeout,
	}
}

// Start 启动代理服务器，开始监听并处理客户端连接。
func (s *Server) Start() error {
	var err error
	s.listener, err = net.Listen("tcp", s.address)
	if err != nil {
		return fmt.Errorf("监听地址(%s)失败: %w", s.address, err)
	}
	// defer 不在此处, Stop() 负责关闭

	log.Printf("隧道代理服务已启动，监听地址: %s\n", s.address)

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// 监听器关闭时退出
			break
		}
		go s.handleConn(conn)
	}
	return nil
}

// Stop 停止代理服务器。
func (s *Server) Stop() {
	if s.listener != nil {
		s.listener.Close()
	}
}

// handleConn 处理单个客户端连接。
// 读取前几个字节自动识别协议类型，然后分发到对应的处理器。
func (s *Server) handleConn(clientConn net.Conn) {
	defer clientConn.Close()

	// 设置读取超时以获取协议头
	_ = clientConn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// 读取足够字节以识别协议
	// CONNECT = 7, DELETE = 6, SOCKS5 = 1
	header := make([]byte, 7)
	n, err := clientConn.Read(header)
	if err != nil {
		log.Printf("读取客户端请求头失败: %v\n", err)
		return
	}

	// 清除超时
	_ = clientConn.SetReadDeadline(time.Time{})

	// 记录已读取的数据
	var buf []byte
	if n > 0 {
		buf = make([]byte, n)
		copy(buf, header[:n])
	}

	// 从池中选取一个节点
	targetNode := s.pool.GetRandom()
	if targetNode == nil {
		log.Println("代理池中没有可用节点")
		return
	}

	// 识别协议并处理
	firstByte := header[0]
	first3 := string(header[:min(3, n)])
	first4 := string(header[:min(4, n)])
	first7 := string(header[:min(7, n)])

	switch {
	case firstByte == 0x05:
		// SOCKS5
		log.Printf("[SOCKS5] 客户端连接，分配到节点: %s\n", targetNode.Name)
		handleSOCKS5(clientConn, buf, s.pool, s.timeout)

	case first7 == "CONNECT":
		// HTTP CONNECT
		log.Printf("[CONNECT] 客户端连接，分配到节点: %s\n", targetNode.Name)
		handleCONNECT(clientConn, buf, s.pool, s.timeout)

	case first3 == "GET" || first4 == "POST" || first3 == "PUT" ||
		first4 == "HEAD" || first4 == "PATC" || first3 == "DEL":
		// 其他 HTTP 方法
		log.Printf("[HTTP] 客户端连接，分配到节点: %s\n", targetNode.Name)
		handleHTTP(clientConn, buf, s.pool, s.timeout)

	default:
		log.Printf("无法识别的协议: %x\n", header[:n])
	}
}

// relay 在本地连接和远程连接之间双向转发数据。
func relay(local, remote net.Conn) error {
	done := make(chan error, 2)

	// 本地 -> 远程
	go func() {
		_, err := io.Copy(remote, local)
		done <- err
	}()

	// 远程 -> 本地
	go func() {
		_, err := io.Copy(local, remote)
		done <- err
	}()

	// 等待任意一个方向完成或出错
	err := <-done
	// 关闭两个连接以终止另一个方向的转发
	_ = local.Close()
	_ = remote.Close()
	// 等待另一个方向完成
	<-done

	return err
}

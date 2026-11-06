// Package proxy 实现隧道代理服务。
// 支持 HTTP CONNECT、普通 HTTP 代理和 SOCKS5 三种协议的自动识别与转发。
// 默认每个新连接从代理池中随机选取一个节点进行转发。
// 固定模式（Fixed）或快速模式（Fast）下，始终使用代理池中钉住的最快节点。
package proxy

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
)

// Server 隧道代理服务器。
type Server struct {
	address    string     // 监听地址，格式为 "host:port"
	pool       *pool.Pool // 代理池
	timeout    int        // 连接超时（毫秒）
	pinFastest bool       // 钉住最快节点：固定模式恒为 true；快速模式由检查器在节点失效时切换
	listener   net.Listener
	stopOnce   sync.Once
}

// NewServer 创建一个新的代理服务器实例。
// addr: 监听地址，如 "127.0.0.1:1080"。
// p: 代理池实例。
// timeout: 节点连接超时（毫秒）。
// pinFastest: 是否钉住代理池中的最快节点（固定模式、快速模式均为 true）。
func NewServer(addr string, p *pool.Pool, timeout int, pinFastest bool) *Server {
	return &Server{
		address:    addr,
		pool:       p,
		timeout:    timeout,
		pinFastest: pinFastest,
	}
}

// Listen 创建监听器。调用成功后端口即被占用，用于在启动阶段同步暴露端口冲突错误。
func (s *Server) Listen() error {
	var err error
	s.listener, err = net.Listen("tcp", s.address)
	if err != nil {
		return fmt.Errorf("监听地址(%s)失败: %w", s.address, err)
	}
	return nil
}

// Serve 开始处理客户端连接，阻塞直到监听器关闭。
// 调用前需先成功调用 Listen。
func (s *Server) Serve() {
	log.Printf("隧道代理服务已启动，监听地址: %s\n", s.address)

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// 监听器关闭时退出
			break
		}
		go s.handleConn(conn)
	}
}

// Start 启动代理服务器，开始监听并处理客户端连接。
func (s *Server) Start() error {
	if err := s.Listen(); err != nil {
		return err
	}
	s.Serve()
	return nil
}

// Stop 停止代理服务器。可重复调用。
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		if s.listener != nil {
			s.listener.Close()
		}
	})
}

// pickNode 从代理池中选取一个节点。
// 钉住最快节点时返回池中钉住的节点（所有连接共享同一个节点，保证出口 IP 固定）；
// 普通模式下随机选取（每次连接可能不同节点，实现负载均衡）。
func (s *Server) pickNode() *node.Node {
	if s.pinFastest {
		return s.pool.GetFastest()
	}
	return s.pool.GetRandom()
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
	targetNode := s.pickNode()
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
		handleSOCKS5(clientConn, buf, targetNode, s.timeout)

	case first7 == "CONNECT":
		// HTTP CONNECT
		log.Printf("[CONNECT] 客户端连接，分配到节点: %s\n", targetNode.Name)
		handleCONNECT(clientConn, buf, targetNode, s.timeout)

	case first3 == "GET" || first4 == "POST" || first3 == "PUT" ||
		first4 == "HEAD" || first4 == "PATC" || first3 == "DEL":
		// 其他 HTTP 方法
		log.Printf("[HTTP] 客户端连接，分配到节点: %s\n", targetNode.Name)
		handleHTTP(clientConn, buf, targetNode, s.timeout)

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

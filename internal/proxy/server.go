// Package proxy 实现隧道代理服务。
// 支持 HTTP CONNECT、普通 HTTP 代理和 SOCKS5 三种协议的自动识别与转发。
// 默认每个新连接从代理池中随机选取一个节点进行转发；
// 客户端在 Proxy-Authorization 中携带会话标识时，同一会话固定复用同一节点（粘性会话），
// 保证出口 IP 稳定，满足 Akamai 等按 IP 绑定 cookie 的站点抓取需求。
// 固定模式（Fixed）或快速模式（Fast）下，始终使用代理池中钉住的最快节点。
package proxy

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
)

// Server 隧道代理服务器。
type Server struct {
	address    string        // 监听地址，格式为 "host:port"
	pool       *pool.Pool    // 代理池
	timeout    int           // 连接超时（毫秒）
	pinFastest bool          // 钉住最快节点：固定模式恒为 true；快速模式由检查器在节点失效时切换
	sticky     bool          // 是否启用粘性会话
	sessions   *sessionTable // 会话键到节点的绑定表
	listener   net.Listener
	stopOnce   sync.Once
}

// Option 代理服务器可选配置。
type Option func(*Server)

// WithStickySession 配置粘性会话。
// enabled 为 false 时忽略客户端的会话标识，退化为纯随机分配；
// ttlSeconds 为会话空闲过期时长，<=0 表示不过期。
func WithStickySession(enabled bool, ttlSeconds int) Option {
	return func(s *Server) {
		s.sticky = enabled
		s.sessions = newSessionTable(ttlSeconds)
	}
}

// WithSessionStore 设置粘性会话绑定的持久化文件路径。
// 设置后代理重启仍能复用原节点，避免已绑定到出口 IP 的 cookie 失效。
func WithSessionStore(path string) Option {
	return func(s *Server) {
		if s.sessions != nil {
			s.sessions.SetStorePath(path)
		}
	}
}

// NewServer 创建一个新的代理服务器实例。
// addr: 监听地址，如 "127.0.0.1:1080"。
// p: 代理池实例。
// timeout: 节点连接超时（毫秒）。
// pinFastest: 是否钉住代理池中的最快节点（固定模式、快速模式均为 true）。
// opts: 可选配置，如 WithStickySession。
func NewServer(addr string, p *pool.Pool, timeout int, pinFastest bool, opts ...Option) *Server {
	s := &Server{
		address:    addr,
		pool:       p,
		timeout:    timeout,
		pinFastest: pinFastest,
		sticky:     true,
		sessions:   newSessionTable(600),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
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
	if s.sticky {
		log.Println("粘性会话已启用：Proxy-Authorization 中的用户名即会话键，同一会话固定复用同一出口节点")
	}

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

// SessionCount 返回当前粘性会话数量，用于运维观测。
func (s *Server) SessionCount() int {
	if s.sessions == nil {
		return 0
	}
	return s.sessions.Count()
}

// pickNode 从代理池中选取一个节点。
// 钉住最快节点时返回池中钉住的节点（所有连接共享同一节点，保证出口 IP 固定）；
// 启用粘性会话且请求带会话键时，按会话键复用节点；其余情况随机选取。
func (s *Server) pickNode(sessionKey string) *node.Node {
	if s.pinFastest {
		return s.pool.GetFastest()
	}
	if s.sticky && sessionKey != "" && s.sessions != nil {
		n, reused := s.sessions.Get(sessionKey, s.pool, s.pool.GetRandom)
		if n != nil {
			action := "新建"
			if reused {
				action = "复用"
			}
			log.Printf("[SESSION] %q %s -> %s\n", sessionKey, action, n.Name)
			return n
		}
	}
	return s.pool.GetRandom()
}

// handleConn 处理单个客户端连接。
// 先识别协议类型，再分发到对应的处理器；HTTP 协议在读取完整请求头后才选取节点，
// 以便从 Proxy-Authorization 中提取粘性会话键。
func (s *Server) handleConn(clientConn net.Conn) {
	defer clientConn.Close()

	// 设置读取超时以获取协议头
	_ = clientConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReaderSize(clientConn, 32*1024)

	first, err := br.Peek(1)
	if err != nil {
		log.Printf("读取客户端请求头失败: %v\n", err)
		return
	}
	_ = clientConn.SetReadDeadline(time.Time{})

	// SOCKS5：会话粒度即连接本身，无需解析会话键
	if first[0] == 0x05 {
		targetNode := s.pickNode("")
		if targetNode == nil {
			log.Println("代理池中没有可用节点")
			return
		}
		log.Printf("[SOCKS5] 客户端连接，分配到节点: %s\n", targetNode.Name)
		handleSOCKS5(clientConn, br, targetNode, s.timeout)
		return
	}

	// HTTP 代理（CONNECT 或普通请求）
	hdr, err := readHTTPRequestHeader(br)
	if err != nil {
		log.Printf("解析客户端请求头失败: %v\n", err)
		sendHTTPError(clientConn, 400, "Bad Request")
		return
	}

	targetNode := s.pickNode(hdr.Session)
	if targetNode == nil {
		log.Println("代理池中没有可用节点")
		sendHTTPError(clientConn, 503, "Service Unavailable")
		return
	}

	switch strings.ToUpper(hdr.Method) {
	case "CONNECT":
		log.Printf("[CONNECT] 客户端连接，分配到节点: %s\n", targetNode.Name)
		handleCONNECT(clientConn, hdr.Target, targetNode, s.timeout)

	case "GET", "POST", "PUT", "HEAD", "PATCH", "DELETE", "OPTIONS":
		log.Printf("[HTTP] 客户端连接，分配到节点: %s\n", targetNode.Name)
		handleHTTP(clientConn, br, hdr, targetNode, s.timeout)

	default:
		log.Printf("不支持的 HTTP 方法: %s\n", hdr.Method)
		sendHTTPError(clientConn, 405, "Method Not Allowed")
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

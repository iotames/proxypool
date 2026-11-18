package node

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultTestURL 默认的连通性测试目标。
const DefaultTestURL = "http://www.google.com"

// TestTarget 解析后的连通性测试目标。
type TestTarget struct {
	Scheme string // http / https
	Host   string
	Port   int
	Path   string
}

// ParseTestTarget 解析连通性测试目标 URL。
// 未写 scheme 时按 http 处理；http 缺省端口 80，https 缺省端口 443。
// 空字符串回退到 DefaultTestURL。
func ParseTestTarget(raw string) (TestTarget, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultTestURL
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return TestTarget{}, fmt.Errorf("测试目标 URL 无效: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return TestTarget{}, fmt.Errorf("测试目标仅支持 http/https: %s", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return TestTarget{}, fmt.Errorf("测试目标缺少主机名: %s", raw)
	}
	port := 80
	if scheme == "https" {
		port = 443
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return TestTarget{}, fmt.Errorf("测试目标端口无效: %s", p)
		}
		port = n
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	return TestTarget{Scheme: scheme, Host: host, Port: port, Path: path}, nil
}

// Addr 返回 "host:port" 形式的连接地址。
func (t TestTarget) Addr() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

// HostHeader 返回 HTTP 请求的 Host 头，非默认端口时携带端口。
func (t TestTarget) HostHeader() string {
	if (t.Scheme == "http" && t.Port == 80) || (t.Scheme == "https" && t.Port == 443) {
		return t.Host
	}
	return t.Addr()
}

// TestOptions 一次连通性测试的全部参数。
// 统一由 NewTestOptions 构造，避免各调用点各自解析目标或拼装阈值。
type TestOptions struct {
	Target     TestTarget
	Timeout    int // 连接/读取超时（毫秒）
	LatencyMax int // 延迟上限（毫秒），<=0 表示不按延迟淘汰
}

// NewTestOptions 由配置项构造测试参数，是所有测试入口的唯一参数来源。
func NewTestOptions(rawURL string, timeout, latencyMax int) (TestOptions, error) {
	target, err := ParseTestTarget(rawURL)
	if err != nil {
		return TestOptions{}, err
	}
	if timeout <= 0 {
		timeout = 300
	}
	if latencyMax < 0 {
		latencyMax = 0
	}
	return TestOptions{Target: target, Timeout: timeout, LatencyMax: latencyMax}, nil
}

// TestResult 存储节点的测试结果。
type TestResult struct {
	Node    *Node
	Latency int64  // 延迟（毫秒）
	Status  string // available / unavailable
	Err     error
}

// TestNode 测试单个节点的连通性。
// http 目标：建链后发送 GET；https 目标：建链后完成 TLS 握手再发送 GET。
// 延迟含 TLS 握手，能反映真实链路可用性；证书校验失败视为不可用。
// 说明：连通性与延迟阈值解耦——慢不等于不可用。
func TestNode(n *Node, opts TestOptions) TestResult {
	n.SetStatus("testing")
	start := time.Now()

	conn, err := DialThroughNode(n, opts.Target.Addr(), opts.Timeout)
	if err != nil {
		n.SetState(-1, "unavailable")
		return TestResult{
			Node:    n,
			Latency: -1,
			Status:  "unavailable",
			Err:     fmt.Errorf("节点(%s)连接失败: %w", n.Name, err),
		}
	}
	defer conn.Close()

	// 设置超时读取（对底层连接生效，TLS 握手与后续读写共用该期限）
	_ = conn.SetDeadline(time.Now().Add(time.Duration(opts.Timeout) * time.Millisecond))

	if opts.Target.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: opts.Target.Host,
			MinVersion: tls.VersionTLS12,
		})
		if err := tlsConn.Handshake(); err != nil {
			n.SetState(-1, "unavailable")
			return TestResult{
				Node:    n,
				Latency: -1,
				Status:  "unavailable",
				Err:     fmt.Errorf("节点(%s)TLS 握手失败: %w", n.Name, err),
			}
		}
		conn = tlsConn
	}

	// 发送简单的 HTTP GET 请求以验证连通性
	reqStr := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: proxypool-health-check\r\nConnection: close\r\n\r\n",
		opts.Target.Path, opts.Target.HostHeader())
	if _, err := conn.Write([]byte(reqStr)); err != nil {
		n.SetState(-1, "unavailable")
		return TestResult{
			Node:    n,
			Latency: -1,
			Status:  "unavailable",
			Err:     fmt.Errorf("节点(%s)发送请求失败: %w", n.Name, err),
		}
	}

	// 读取响应
	buf := make([]byte, 256)
	_, err = conn.Read(buf)
	if err != nil {
		n.SetState(-1, "unavailable")
		return TestResult{
			Node:    n,
			Latency: -1,
			Status:  "unavailable",
			Err:     fmt.Errorf("节点(%s)读取响应失败: %w", n.Name, err),
		}
	}

	// 验证响应中包含有效的 HTTP 响应
	responseStr := string(buf[:])
	if strings.Contains(responseStr, "HTTP/") {
		latency := time.Since(start).Milliseconds()
		// 连通成功：延迟达标（或未启用上限）即为可用；上限内保留实测延迟用于展示
		if opts.LatencyMax > 0 && latency > int64(opts.LatencyMax) {
			n.SetState(latency, "unavailable")
			return TestResult{
				Node:    n,
				Latency: latency,
				Status:  "unavailable",
				Err:     fmt.Errorf("节点(%s)延迟 %dms 超过上限 %dms", n.Name, latency, opts.LatencyMax),
			}
		}
		n.SetState(latency, "available")
		return TestResult{
			Node:    n,
			Latency: latency,
			Status:  "available",
		}
	}

	n.SetState(-1, "unavailable")
	return TestResult{
		Node:    n,
		Latency: -1,
		Status:  "unavailable",
		Err:     fmt.Errorf("节点(%s)返回无效响应", n.Name),
	}
}

// TestNodes 并发测试多个节点的连通性。
func TestNodes(nodes []*Node, opts TestOptions) []TestResult {
	results := make([]TestResult, 0, len(nodes))
	resultCh := make(chan TestResult, len(nodes))

	for _, n := range nodes {
		n.SetStatus("testing")
		go func(node *Node) {
			resultCh <- TestNode(node, opts)
		}(n)
	}

	for i := 0; i < len(nodes); i++ {
		results = append(results, <-resultCh)
	}
	close(resultCh)

	return results
}

// TestNodeConnect 仅测试节点 TCP 连接性（不发送 HTTP 请求）。
// 用于快速检测节点是否在线。
func TestNodeConnect(n *Node, timeout int) TestResult {
	start := time.Now()

	conn, err := net.DialTimeout("tcp", n.Addr(), time.Duration(timeout)*time.Millisecond)
	if err != nil {
		n.SetState(-1, "unavailable")
		return TestResult{
			Node:    n,
			Latency: -1,
			Status:  "unavailable",
			Err:     fmt.Errorf("节点(%s)TCP连接失败: %w", n.Name, err),
		}
	}
	conn.Close()

	latency := time.Since(start).Milliseconds()
	n.SetState(latency, "available")
	return TestResult{
		Node:    n,
		Latency: latency,
		Status:  "available",
	}
}

package node

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// defaultTestTarget 默认的连通性测试目标。
const defaultTestTarget = "https://www.google.com"

// TestResult 存储节点的测试结果。
type TestResult struct {
	Node    *Node
	Latency int64  // 延迟（毫秒）
	Status  string // available / unavailable
	Err     error
}

// TestNode 测试单个节点的连通性。
// 通过节点隧道连接到测试目标，测量延迟。
// timeout: 超时时间（毫秒）。
func TestNode(n *Node, timeout int) TestResult {
	n.Status = "testing"
	start := time.Now()

	// 通过代理节点连接到测试目标（使用 HTTP 端口 80，避免 TLS 握手）
	targetAddr := "www.google.com:80"
	conn, err := DialThroughNode(n, targetAddr, timeout)
	if err != nil {
		n.Status = "unavailable"
		n.Latency = -1
		return TestResult{
			Node:    n,
			Latency: -1,
			Status:  "unavailable",
			Err:     fmt.Errorf("节点(%s)连接失败: %w", n.Name, err),
		}
	}
	defer conn.Close()

	// 设置超时读取
	_ = conn.SetDeadline(time.Now().Add(time.Duration(timeout) * time.Millisecond))

	// 发送简单的 HTTP GET 请求以验证连通性
	reqStr := "GET / HTTP/1.1\r\nHost: www.google.com\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(reqStr)); err != nil {
		n.Status = "unavailable"
		n.Latency = -1
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
		n.Status = "unavailable"
		n.Latency = -1
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
		n.Status = "available"
		n.Latency = latency
		return TestResult{
			Node:    n,
			Latency: latency,
			Status:  "available",
		}
	}

	n.Status = "unavailable"
	n.Latency = -1
	return TestResult{
		Node:    n,
		Latency: -1,
		Status:  "unavailable",
		Err:     fmt.Errorf("节点(%s)返回无效响应", n.Name),
	}
}

// TestNodes 并发测试多个节点的连通性。
// timeout: 每个节点的超时时间（毫秒）。
func TestNodes(nodes []*Node, timeout int) []TestResult {
	results := make([]TestResult, 0, len(nodes))
	resultCh := make(chan TestResult, len(nodes))

	for _, n := range nodes {
		n.Status = "testing"
		go func(node *Node) {
			resultCh <- TestNode(node, timeout)
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
		return TestResult{
			Node:    n,
			Latency: -1,
			Status:  "unavailable",
			Err:     fmt.Errorf("节点(%s)TCP连接失败: %w", n.Name, err),
		}
	}
	conn.Close()

	latency := time.Since(start).Milliseconds()
	n.Latency = latency
	n.Status = "available"
	return TestResult{
		Node:    n,
		Latency: latency,
		Status:  "available",
	}
}

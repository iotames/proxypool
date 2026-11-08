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
// 通过节点隧道连接到测试目标，测量总延迟。
// timeout:   连接/读取超时（毫秒）。
// latencyMax: 延迟上限（毫秒）。<=0 表示不按延迟淘汰（连通即视为可用，
// 延迟仅用于排序展示）；>0 时总延迟超过该上限才判为不可用。
// 说明：连通性与延迟阈值解耦——慢不等于不可用。
func TestNode(n *Node, timeout, latencyMax int) TestResult {
	n.SetStatus("testing")
	start := time.Now()

	// 通过代理节点连接到测试目标（使用 HTTP 端口 80，避免 TLS 握手）
	targetAddr := "www.google.com:80"
	conn, err := DialThroughNode(n, targetAddr, timeout)
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

	// 设置超时读取
	_ = conn.SetDeadline(time.Now().Add(time.Duration(timeout) * time.Millisecond))

	// 发送简单的 HTTP GET 请求以验证连通性
	reqStr := "GET / HTTP/1.1\r\nHost: www.google.com\r\nConnection: close\r\n\r\n"
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
		if latencyMax > 0 && latency > int64(latencyMax) {
			n.SetState(latency, "unavailable")
			return TestResult{
				Node:    n,
				Latency: latency,
				Status:  "unavailable",
				Err:     fmt.Errorf("节点(%s)延迟 %dms 超过上限 %dms", n.Name, latency, latencyMax),
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
// timeout: 每个节点的连接/读取超时（毫秒）。
// latencyMax: 延迟上限（毫秒），<=0 表示不按延迟淘汰。
func TestNodes(nodes []*Node, timeout, latencyMax int) []TestResult {
	results := make([]TestResult, 0, len(nodes))
	resultCh := make(chan TestResult, len(nodes))

	for _, n := range nodes {
		n.SetStatus("testing")
		go func(node *Node) {
			resultCh <- TestNode(node, timeout, latencyMax)
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

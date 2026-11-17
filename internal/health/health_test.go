package health

import (
	"testing"

	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
)

// TestCheckerAddUnavailableKeepsNodeIdentity 回归测试：健康检查必须保存原始节点指针。
// 历史缺陷：AddUnavailable 保存 Clone，恢复后把副本加入代理池，
// 导致 /api/status 的池大小与 /api/nodes 的节点状态不一致。
func TestCheckerAddUnavailableKeepsNodeIdentity(t *testing.T) {
	p := pool.New(0)
	c := NewChecker(p, 60, 300, 0)
	n := &node.Node{Name: "n1", Type: "trojan", Server: "127.0.0.1", Port: 1}
	n.SetState(-1, "unavailable")

	c.AddUnavailable(n)

	got, ok := c.unavailable[n.Name]
	if !ok {
		t.Fatal("节点未加入待恢复列表")
	}
	if got != n {
		t.Fatal("待恢复列表应保存原始节点指针，不应保存副本")
	}

	// 模拟恢复：同一对象状态更新后加入池，池内与展示列表必须是同一份状态。
	n.SetState(120, "available")
	if !p.Add(n) {
		t.Fatal("恢复节点应能加入代理池")
	}
	if n.Status() != "available" || n.Latency() != 120 {
		t.Fatalf("节点状态应保持可用，实际 status=%s latency=%d", n.Status(), n.Latency())
	}
}

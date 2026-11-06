package health

import (
	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
)

// RecheckAll 全量重测所有节点，并按最新结果更新、重排代理池。
// 可用节点重新入池，不可用或延迟超过阈值的节点移出。
// 返回总节点数与更新后的可用节点数。
func RecheckAll(p *pool.Pool, nodes []*node.Node, timeout int) (total, available int) {
	results := node.TestNodes(nodes, timeout)
	for _, result := range results {
		if result.Status == "available" {
			// 已存在的节点延迟会被就地更新；新恢复的节点重新入池
			p.Add(result.Node)
		} else {
			p.Remove(result.Node.Name)
		}
	}
	p.Reorder()
	return len(nodes), p.Size()
}

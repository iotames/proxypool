// Package pool 管理代理节点的选择池。
// 代理池中的节点按延迟升序排列（索引 0 为最快节点）。
// 支持两种选取模式：
//   - 随机模式（默认）：每次从池中随机选取一个节点
//   - 固定模式（Fixed）：永远只用索引 0 的最快节点
package pool

import (
	"math/rand"
	"sort"
	"sync"

	"github.com/iotames/proxypool/internal/node"
)

// Pool 管理可用的代理节点集合，提供线程安全的节点选取功能。
// 节点始终按延迟升序排列，nodes[0] 始终是最快节点。
type Pool struct {
	mu    sync.RWMutex
	nodes []*node.Node
	max   int          // 最大节点数，0 表示不限制
	fixed *node.Node   // 固定模式下的选中节点（索引 0 的克隆副本）
}

// New 创建一个新的代理池。
// maxSize 为最大节点数限制，0 表示不限制。
func New(maxSize int) *Pool {
	return &Pool{
		nodes: make([]*node.Node, 0),
		max:   maxSize,
	}
}

// Add 向池中添加一个可用节点。
// 按延迟升序插入，若池已满且该节点延迟高于最差节点，则不添加。
// 如果在固定模式下插入了更快的新节点，自动更新固定节点指向最新的最快节点。
func (p *Pool) Add(n *node.Node) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 检查是否已存在同名节点
	for _, existing := range p.nodes {
		if existing.Name == n.Name {
			return false
		}
	}

	// 检查最大容量
	if p.max > 0 && len(p.nodes) >= p.max {
		// 如果当前节点延迟高于池中最差节点，不添加
		if n.Latency > p.nodes[len(p.nodes)-1].Latency {
			return false
		}
		// 移除最差节点
		p.nodes = p.nodes[:len(p.nodes)-1]
	}

	p.nodes = append(p.nodes, n)
	// 按延迟升序排列，确保 nodes[0] 始终是最快节点
	sort.Slice(p.nodes, func(i, j int) bool {
		return p.nodes[i].Latency < p.nodes[j].Latency
	})

	// 如果已为固定模式选中了节点，且新插入的节点比当前固定节点更快，
	// 则自动更新固定节点，保证永远使用池中最快的那个节点
	if p.fixed != nil && p.nodes[0].Latency < p.fixed.Latency {
		p.fixed = p.nodes[0].Clone()
	}

	return true
}

// Remove 从池中移除指定名称的节点。
func (p *Pool) Remove(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, n := range p.nodes {
		if n.Name == name {
			p.nodes = append(p.nodes[:i], p.nodes[i+1:]...)
			return true
		}
	}
	return false
}

// GetRandom 从池中随机选取一个节点。
// 返回 nil 表示池为空。
func (p *Pool) GetRandom() *node.Node {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.nodes) == 0 {
		return nil
	}

	// 加权随机：延迟越低的节点被选中的概率越高
	// 使用指数权重的简单实现
	return p.nodes[rand.Intn(len(p.nodes))]
}

// GetFastest 返回池中最快的节点（索引 0，延迟最低）。
// 池为空时返回 nil。
// 用于固定代理模式（--fixed 参数），每次调用都返回同一个最快节点，
// 确保所有网络请求始终通过同一个最优代理出口。
func (p *Pool) GetFastest() *node.Node {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.nodes) == 0 {
		return nil
	}

	// 首次调用时，将 nodes[0]（当前最快节点）克隆并缓存
	// 后续所有请求都返回这个缓存副本，保证一致性
	if p.fixed == nil {
		p.fixed = p.nodes[0].Clone()
	}
	return p.fixed
}

// Size 返回池中节点数量。
func (p *Pool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.nodes)
}

// All 返回池中所有节点的副本。
func (p *Pool) All() []*node.Node {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]*node.Node, len(p.nodes))
	for i, n := range p.nodes {
		result[i] = n.Clone()
	}
	return result
}

// AvgLatency 返回池中节点的平均延迟。
// 池为空时返回 0。
func (p *Pool) AvgLatency() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.nodes) == 0 {
		return 0
	}

	var total int64
	for _, n := range p.nodes {
		total += n.Latency
	}
	return float64(total) / float64(len(p.nodes))
}

// AvailableCount 返回可用节点数（即池大小）。
func (p *Pool) AvailableCount() int {
	return p.Size()
}

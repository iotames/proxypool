// Package health 提供代理节点的健康检查和恢复功能。
// 定时重新测试已被剔除的节点，成功后重新加入代理池。
package health

import (
	"log"
	"time"

	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
)

// Checker 健康检查器，定期重试不可用节点。
type Checker struct {
	pool         *pool.Pool      // 代理池
	unavailable  map[string]*node.Node // 不可用的节点列表
	interval     int             // 检查间隔（秒）
	timeout      int             // 测试超时（毫秒）
	stopCh       chan struct{}
}

// NewChecker 创建一个新的健康检查器。
// p: 代理池，用于将恢复的节点重新加入。
// interval: 检查间隔（秒）。
// timeout: 节点测试超时（毫秒）。
func NewChecker(p *pool.Pool, interval, timeout int) *Checker {
	return &Checker{
		pool:        p,
		unavailable: make(map[string]*node.Node),
		interval:    interval,
		timeout:     timeout,
		stopCh:      make(chan struct{}),
	}
}

// AddUnavailable 将不可用的节点加入待恢复列表。
func (c *Checker) AddUnavailable(n *node.Node) {
	if n == nil {
		return
	}
	c.unavailable[n.Name] = n.Clone()
	log.Printf("节点(%s)已加入健康检查待恢复列表\n", n.Name)
}

// Start 启动定时健康检查循环。
func (c *Checker) Start() {
	ticker := time.NewTicker(time.Duration(c.interval) * time.Second)
	defer ticker.Stop()

	log.Printf("健康检查已启动，间隔: %d 秒\n", c.interval)

	for {
		select {
		case <-ticker.C:
			c.check()
		case <-c.stopCh:
			log.Println("健康检查已停止")
			return
		}
	}
}

// Stop 停止健康检查。
func (c *Checker) Stop() {
	close(c.stopCh)
}

// check 执行一次健康检查，重试所有不可用节点。
func (c *Checker) check() {
	if len(c.unavailable) == 0 {
		return
	}

	log.Printf("开始健康检查，待恢复节点数: %d\n", len(c.unavailable))

	var nodes []*node.Node
	for _, n := range c.unavailable {
		nodes = append(nodes, n)
	}

	results := node.TestNodes(nodes, c.timeout)
	for _, result := range results {
		if result.Status == "available" {
			if c.pool.Add(result.Node) {
				delete(c.unavailable, result.Node.Name)
				log.Printf("节点(%s)已恢复，延迟: %dms\n", result.Node.Name, result.Latency)
			}
		} else {
			log.Printf("节点(%s)仍然不可用: %v\n", result.Node.Name, result.Err)
		}
	}
}

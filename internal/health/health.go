// Package health 提供代理节点的健康检查和恢复功能。
// 定时重新测试已被剔除的节点，成功后重新加入代理池。
package health

import (
	"log"
	"sync"
	"time"

	"github.com/iotames/proxypool/internal/config"
	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
)

// Checker 健康检查器，定期重试不可用节点。
type Checker struct {
	pool        *pool.Pool            // 代理池
	unavailable map[string]*node.Node // 不可用的节点列表
	cfg         *config.Conf          // 配置源，每轮读取快照以支持热更新
	mu          sync.Mutex            // 保护 unavailable
	stopCh      chan struct{}
	stopOnce    sync.Once
}

// NewChecker 创建一个新的健康检查器。
// p: 代理池，用于将恢复的节点重新加入。
// cfg: 配置源，巡检参数每轮从快照读取。
func NewChecker(p *pool.Pool, cfg *config.Conf) *Checker {
	return &Checker{
		pool:        p,
		unavailable: make(map[string]*node.Node),
		cfg:         cfg,
		stopCh:      make(chan struct{}),
	}
}

// intervalOf 返回下一次检查间隔（秒），非法值回退 60 秒。
func intervalOf(cfg *config.Conf) int {
	iv := cfg.Snapshot().HealthInterval
	if iv <= 0 {
		return 60
	}
	return iv
}

// testOptions 由配置快照构造统一的测试参数。
func testOptions(cfg *config.Conf) (node.TestOptions, error) {
	snap := cfg.Snapshot()
	return node.NewTestOptions(snap.TestURL, snap.GoogleTimeout, snap.LatencyMax)
}

// AddUnavailable 将不可用的节点加入待恢复列表。
// 必须保存原始节点指针而非副本：代理池与 /api/nodes 共享同一节点对象，
// 使用副本会让恢复后的节点在池中与展示列表中变成两份独立状态。
func (c *Checker) AddUnavailable(n *node.Node) {
	if n == nil {
		return
	}
	c.mu.Lock()
	c.unavailable[n.Name] = n
	c.mu.Unlock()
	log.Printf("节点(%s)已加入健康检查待恢复列表\n", n.Name)
}

// Start 启动定时健康检查循环。
func (c *Checker) Start() {
	log.Printf("健康检查已启动，间隔: %d 秒\n", intervalOf(c.cfg))

	for {
		timer := time.NewTimer(time.Duration(intervalOf(c.cfg)) * time.Second)
		select {
		case <-timer.C:
			c.check()
		case <-c.stopCh:
			timer.Stop()
			log.Println("健康检查已停止")
			return
		}
	}
}

// Stop 停止健康检查。可重复调用。
func (c *Checker) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
	})
}

// check 执行一次健康检查，重试所有不可用节点。
func (c *Checker) check() {
	c.mu.Lock()
	if len(c.unavailable) == 0 {
		c.mu.Unlock()
		return
	}
	nodes := make([]*node.Node, 0, len(c.unavailable))
	for _, n := range c.unavailable {
		nodes = append(nodes, n)
	}
	c.mu.Unlock()

	log.Printf("开始健康检查，待恢复节点数: %d\n", len(nodes))

	opts, err := testOptions(c.cfg)
	if err != nil {
		log.Printf("健康检查已跳过: %v\n", err)
		return
	}
	results := node.TestNodes(nodes, opts)
	for _, result := range results {
		if result.Status == "available" {
			if c.pool.Add(result.Node) {
				c.mu.Lock()
				delete(c.unavailable, result.Node.Name)
				c.mu.Unlock()
				log.Printf("节点(%s)已恢复，延迟: %dms\n", result.Node.Name, result.Latency)
			}
		} else {
			log.Printf("节点(%s)仍然不可用: %v\n", result.Node.Name, result.Err)
		}
	}
}

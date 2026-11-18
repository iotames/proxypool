package health

import (
	"log"
	"sync"
	"time"

	"github.com/iotames/proxypool/internal/config"
	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
)

// FastChecker 快速模式健康检查器。
// 按设定周期重测全部节点并维护代理池，兼顾出口 IP 稳定与节点可用性：
//   - 当前固定节点仍可用时保持不变，避免出口 IP 频繁跳动触发验证码；
//   - 当前固定节点不可用（掉出可用池）时，自动切换到池中最快节点。
type FastChecker struct {
	pool     *pool.Pool   // 代理池
	nodes    []*node.Node // 全部节点（含已被剔除的节点，用于恢复）
	cfg      *config.Conf // 配置源，每轮读取快照以支持热更新
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewFastChecker 创建一个快速模式健康检查器。
// p: 代理池。
// nodes: 配置文件中的全部节点。
// cfg: 配置源，巡检参数每轮从快照读取。
func NewFastChecker(p *pool.Pool, nodes []*node.Node, cfg *config.Conf) *FastChecker {
	return &FastChecker{
		pool:   p,
		nodes:  nodes,
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

// Start 启动定时重测循环。
func (c *FastChecker) Start() {
	log.Printf("快速模式健康检查已启动，间隔: %d 秒\n", intervalOf(c.cfg))

	for {
		timer := time.NewTimer(time.Duration(intervalOf(c.cfg)) * time.Second)
		select {
		case <-timer.C:
			c.check()
		case <-c.stopCh:
			timer.Stop()
			log.Println("快速模式健康检查已停止")
			return
		}
	}
}

// Stop 停止快速模式健康检查。可重复调用。
func (c *FastChecker) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
	})
}

// check 重测全部节点，更新代理池，并决定是否切换固定节点。
func (c *FastChecker) check() {
	current := c.pool.GetFastest()
	currentName := ""
	if current != nil {
		currentName = current.Name
	}

	opts, err := testOptions(c.cfg)
	if err != nil {
		log.Printf("快速模式健康检查已跳过: %v\n", err)
		return
	}
	results := node.TestNodes(c.nodes, opts)
	for _, result := range results {
		if result.Status == "available" {
			// 已在池中的节点延迟由共享指针就地更新；新恢复的节点则重新入池
			c.pool.Add(result.Node)
		} else {
			c.pool.Remove(result.Node.Name)
		}
	}

	// 原固定节点仍在可用池中：保持不变，稳定出口 IP
	if currentName != "" && c.pool.GetByName(currentName) != nil {
		if n := c.pool.PinFastest(currentName); n != nil {
			log.Printf("快速模式：保持节点 %s（延迟 %dms）\n", n.Name, n.Latency())
		}
		return
	}

	fastest := c.pool.ReselectFastest()
	if fastest == nil {
		log.Println("快速模式：当前没有可用节点，等待节点恢复")
		return
	}
	if currentName == "" {
		log.Printf("快速模式：选定最快节点 %s（延迟 %dms）\n", fastest.Name, fastest.Latency())
	} else {
		log.Printf("快速模式：节点 %s 已不可用，切换最快节点为 %s（延迟 %dms）\n",
			currentName, fastest.Name, fastest.Latency())
	}
}

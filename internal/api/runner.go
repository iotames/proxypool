package api

import (
	"fmt"
	"log"

	"github.com/iotames/proxypool/internal/clash"
	"github.com/iotames/proxypool/internal/config"
	"github.com/iotames/proxypool/internal/health"
	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
	"github.com/iotames/proxypool/internal/proxy"
)

// runProxy 执行完整的代理初始化流程：解析配置→测试节点→构建池→启动服务。
// confDir: .env 文件所在目录。
// cfg: 运行配置。
// p: 代理池（预先创建）。
// progressCh: 进度消息通道（可为 nil）。
// stopCh: 停止信号通道。
func runProxy(confDir string, cfg *config.Conf, p *pool.Pool, progressCh chan<- string, stopCh <-chan struct{}) error {
	log.Println("=== 开始启动隧道代理 ===")
	sendProgress(progressCh, "正在读取 Clash 配置...")

	allNodes, err := clash.ParseFile(cfg.ConfPath)
	if err != nil {
		sendProgress(progressCh, fmt.Sprintf("❌ 解析 Clash 配置文件失败: %v", err))
		return fmt.Errorf("解析 Clash 配置文件失败: %w", err)
	}
	if len(allNodes) == 0 {
		sendProgress(progressCh, "❌ 未找到有效的 ss 或 trojan 节点")
		return fmt.Errorf("未找到有效节点")
	}
	sendProgress(progressCh, fmt.Sprintf("✅ 发现 %d 个节点（ss + trojan）", len(allNodes)))

	// 检查是否被要求停止
	select {
	case <-stopCh:
		sendProgress(progressCh, "⏹ 已取消启动")
		return nil
	default:
	}

	sendProgress(progressCh, "正在测试节点连通性...")
	results := node.TestNodes(allNodes, cfg.GoogleTimeout)

	healthChecker := health.NewChecker(p, cfg.HealthInterval, cfg.GoogleTimeout)

	availableCount := 0
	for _, result := range results {
		if result.Status == "available" {
			p.Add(result.Node)
			availableCount++
			sendProgress(progressCh, fmt.Sprintf("  ✅ %s (%dms)", result.Node.Name, result.Latency))
		} else {
			healthChecker.AddUnavailable(result.Node)
		}
	}

	select {
	case <-stopCh:
		sendProgress(progressCh, "⏹ 已取消启动")
		return nil
	default:
	}

	if p.Size() == 0 {
		sendProgress(progressCh, "❌ 没有可用节点")
		return fmt.Errorf("没有可用节点")
	}

	sendProgress(progressCh, fmt.Sprintf("✅ 可用节点: %d / %d", availableCount, len(allNodes)))

	// 启动代理服务
	proxyAddr := fmt.Sprintf("%s:%d", cfg.BindAddress, cfg.Port)
	proxyServer := proxy.NewServer(proxyAddr, p, cfg.GoogleTimeout)

	select {
	case <-stopCh:
		sendProgress(progressCh, "⏹ 已取消启动")
		return nil
	default:
	}

	go func() {
		if err := proxyServer.Start(); err != nil {
			log.Printf("代理服务异常退出: %v\n", err)
		}
	}()

	go healthChecker.Start()

	sendProgress(progressCh, fmt.Sprintf("🚀 隧道代理已启动: http://%s", proxyAddr))
	return nil
}

func sendProgress(ch chan<- string, msg string) {
	if ch == nil {
		return
	}
	log.Println(msg)
	select {
	case ch <- msg:
	default:
	}
}

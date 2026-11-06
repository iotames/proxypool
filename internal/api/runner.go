package api

import (
	"errors"
	"fmt"
	"log"

	"github.com/iotames/proxypool/internal/clash"
	"github.com/iotames/proxypool/internal/config"
	"github.com/iotames/proxypool/internal/health"
	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
	"github.com/iotames/proxypool/internal/proxy"
)

// ErrStartCancelled 表示启动流程被停止操作取消。
var ErrStartCancelled = errors.New("启动已取消")

// RunResult 一次代理启动流程的产物，供调用方持有并在停止时释放资源。
type RunResult struct {
	Nodes  []*node.Node    // 本次解析到的全部节点（含不可用节点）
	Proxy  *proxy.Server   // 已成功监听的代理服务，未启动成功时为 nil
	Health *health.Checker // 已创建的健康检查器，未启动成功时为 nil
}

// runProxy 执行完整的代理初始化流程：解析配置→测试节点→构建池→启动服务。
// confDir: .env 文件所在目录。
// cfg: 运行配置。
// p: 代理池（预先创建）。
// progressCh: 进度消息通道（可为 nil）。
// stopCh: 停止信号通道。
// 返回本次解析到的全部节点与启动产物；启动被停止时返回 ErrStartCancelled。
func runProxy(confDir string, cfg *config.Conf, p *pool.Pool, progressCh chan<- string, stopCh <-chan struct{}) (*RunResult, error) {
	log.Println("=== 开始启动隧道代理 ===")
	sendProgress(progressCh, "正在读取 Clash 配置...")

	allNodes, err := clash.ParseFile(cfg.ConfPath)
	if err != nil {
		sendProgress(progressCh, fmt.Sprintf("❌ 解析 Clash 配置文件失败: %v", err))
		return nil, fmt.Errorf("解析 Clash 配置文件失败: %w", err)
	}
	if len(allNodes) == 0 {
		sendProgress(progressCh, "❌ 未找到有效的 ss 或 trojan 节点")
		return &RunResult{Nodes: allNodes}, fmt.Errorf("未找到有效节点")
	}
	sendProgress(progressCh, fmt.Sprintf("✅ 发现 %d 个节点（ss + trojan）", len(allNodes)))

	// 检查是否被要求停止
	select {
	case <-stopCh:
		sendProgress(progressCh, "⏹ 已取消启动")
		return &RunResult{Nodes: allNodes}, ErrStartCancelled
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
		return &RunResult{Nodes: allNodes}, ErrStartCancelled
	default:
	}

	if p.Size() == 0 {
		sendProgress(progressCh, "❌ 没有可用节点")
		return &RunResult{Nodes: allNodes}, fmt.Errorf("没有可用节点")
	}

	sendProgress(progressCh, fmt.Sprintf("✅ 可用节点: %d / %d", availableCount, len(allNodes)))

	// 启动代理服务：先同步监听，端口占用等问题直接反馈给调用方
	proxyAddr := fmt.Sprintf("%s:%d", cfg.BindAddress, cfg.Port)
	proxyServer := proxy.NewServer(proxyAddr, p, cfg.GoogleTimeout, false)

	if err := proxyServer.Listen(); err != nil {
		sendProgress(progressCh, fmt.Sprintf("❌ 代理端口监听失败: %v", err))
		return &RunResult{Nodes: allNodes}, err
	}

	select {
	case <-stopCh:
		proxyServer.Stop()
		sendProgress(progressCh, "⏹ 已取消启动")
		return &RunResult{Nodes: allNodes}, ErrStartCancelled
	default:
	}

	go proxyServer.Serve()
	go healthChecker.Start()

	sendProgress(progressCh, fmt.Sprintf("🚀 隧道代理已启动: http://%s", proxyAddr))
	return &RunResult{Nodes: allNodes, Proxy: proxyServer, Health: healthChecker}, nil
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

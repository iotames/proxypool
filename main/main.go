// proxypool 隧道代理工具
//
// 两种运行模式：
//
// 1. 命令行模式（CLI）— 直接启动隧道代理：
//
//	cd main
//	./proxypool --port=1080 --conf=clash.yaml
//
// 2. Web 控制台模式 — 启动管理界面，通过按钮启停代理：
//
//	cd main
//	./proxypool
//
// 构建方式：
//
//	go build -o main/proxypool ./main/
//
// 命令行参数：
//   --port   int    代理服务监听端口（默认 1080）
//   --conf  string Clash 配置文件路径（默认 clash.yaml）
//
// 配置项（.env 文件 / 环境变量）：
//   GOOGLE_TIMEOUT    int    谷歌连通性测试超时/毫秒（默认 230）
//   HEALTH_INTERVAL   int    健康检查间隔/秒（默认 60）
//   POOL_MAX_SIZE     int    代理池最大节点数（默认 0=不限制）
//   BIND_ADDRESS      string 代理服务绑定 IP（默认 127.0.0.1）
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/iotames/proxypool/internal/api"
	"github.com/iotames/proxypool/internal/clash"
	"github.com/iotames/proxypool/internal/config"
	"github.com/iotames/proxypool/internal/health"
	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
	"github.com/iotames/proxypool/internal/proxy"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	confDir := getConfDir()

	// 无参数 → Web 控制台模式
	// 有参数（--port / --conf）→ 命令行模式
	if len(os.Args) <= 1 {
		runWebMode(confDir)
	} else {
		runCLIMode(confDir)
	}
}

// runWebMode 启动 Web 控制台模式。
// 只加载配置和 API 服务，用户通过页面按钮启停代理。
func runWebMode(confDir string) {
	cfg, err := config.Load(confDir, false)
	if err != nil {
		log.Fatalf("加载配置失败: %v\n", err)
	}

	proxyPool := pool.New(0)
	apiAddr := fmt.Sprintf("%s:%d", cfg.BindAddress, cfg.Port+1)

	cfg.PrintConfig()

	log.Println("========================================")
	log.Println("proxypool Web 控制台模式")
	log.Printf("  管理地址: http://%s", apiAddr)
	log.Println("  在浏览器中打开以上地址进行操作")
	log.Println("========================================")

	apiServer := api.NewServer(cfg, proxyPool)
	go func() {
		if err := apiServer.Start(apiAddr, false); err != nil {
			log.Fatalf("API 服务启动失败: %v\n", err)
		}
	}()

	// 短暂等待后自动打开浏览器
	if runtime.GOOS == "windows" {
		time.Sleep(800 * time.Millisecond)
		openBrowser("http://" + apiAddr)
	}

	// 等待退出
	select {}
}

// openBrowser 在默认浏览器中打开 URL。
func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		exec.Command("open", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

// runCLIMode 启动命令行模式。
// 完整流程：加载配置 → 解析 Clash → 测试节点 → 构建池 → 启动代理。
func runCLIMode(confDir string) {
	cfg, err := config.Load(confDir, true)
	if err != nil {
		log.Fatalf("加载配置失败: %v\n", err)
	}

	log.Printf("配置加载完成: 端口=%d, 配置文件=%s, 超时=%dms\n",
		cfg.Port, cfg.ConfPath, cfg.GoogleTimeout)

	cfg.PrintConfig()

	// 固定代理模式提示
	if cfg.Fixed {
		log.Println("========================================")
		log.Println("固定代理模式已启用（--fixed）")
		log.Println("  特点：只取测速最快的节点，不随机分配，不出国IP不变")
		log.Println("  注意：不会自动重新测速（忽略 HEALTH_INTERVAL 参数）")
		log.Println("========================================")
	}

	// 解析 Clash 配置文件
	if !filepath.IsAbs(cfg.ConfPath) {
		cfg.ConfPath = filepath.Join(confDir, cfg.ConfPath)
	}
	log.Printf("正在解析 Clash 配置文件: %s\n", cfg.ConfPath)
	allNodes, err := clash.ParseFile(cfg.ConfPath)
	if err != nil {
		log.Fatalf("解析 Clash 配置文件失败: %v\n", err)
	}
	if len(allNodes) == 0 {
		log.Fatalln("未在配置文件中找到有效的 ss 或 trojan 节点")
	}
	log.Printf("共发现 %d 个节点（ss + trojan）\n", len(allNodes))

	// 测试节点连通性
	log.Println("正在测试节点连通性（请稍候）...")
	results := node.TestNodes(allNodes, cfg.GoogleTimeout)

	proxyPool := pool.New(cfg.PoolMaxSize)

	availableCount := 0
	for _, result := range results {
		if result.Status == "available" {
			proxyPool.Add(result.Node)
			availableCount++
			log.Printf("节点(%s) 可用，延迟: %dms\n", result.Node.Name, result.Latency)
		} else {
			log.Printf("节点(%s) 不可用: %v\n", result.Node.Name, result.Err)
		}
	}
	if proxyPool.Size() == 0 {
		log.Fatalln("没有可用节点，无法启动代理服务")
	}
	log.Printf("代理池构建完成: 可用 %d / 总数 %d\n", availableCount, len(allNodes))

	// 固定模式：取最快节点后打印提示
	if cfg.Fixed {
		fastest := proxyPool.GetFastest()
		log.Printf("固定节点已选定: %s（延迟 %dms）\n", fastest.Name, fastest.Latency)
	}

	// 启动代理服务
	proxyAddr := fmt.Sprintf("%s:%d", cfg.BindAddress, cfg.Port)
	proxyServer := proxy.NewServer(proxyAddr, proxyPool, cfg.GoogleTimeout, cfg.Fixed)
	go func() {
		if err := proxyServer.Start(); err != nil {
			log.Fatalf("代理服务启动失败: %v\n", err)
		}
	}()

	// 启动健康检查（固定模式下不启动，因为只测速一次，后续不再自动测速）
	if !cfg.Fixed {
		healthChecker := health.NewChecker(proxyPool, cfg.HealthInterval, cfg.GoogleTimeout)
		// 将不可用节点加入健康检查待恢复列表，等待后续自动恢复
		for _, result := range results {
			if result.Status != "available" {
				healthChecker.AddUnavailable(result.Node)
			}
		}
		go healthChecker.Start()
	} else {
		log.Println("固定模式：跳过健康检查（HEALTH_INTERVAL 已忽略）")
	}

	// 启动 API 服务
	apiAddr := fmt.Sprintf("%s:%d", cfg.BindAddress, cfg.Port+1)
	apiServer := api.NewServer(cfg, proxyPool)
	apiServer.SetProxy(proxyServer)
	go func() {
		if err := apiServer.Start(apiAddr, true); err != nil {
			log.Printf("API 服务已停止: %v\n", err)
		}
	}()

	log.Println("========================================")
	log.Printf("proxypool 隧道代理已启动")
	log.Printf("  代理地址: http://%s", proxyAddr)
	log.Printf("  API 地址: http://%s", apiAddr)
	log.Printf("  代理池节点: %d", proxyPool.Size())
	if cfg.Fixed {
		log.Printf("  模式: 固定代理（节点: %s）", proxyPool.GetFastest().Name)
	} else {
		log.Printf("  模式: 随机分配")
	}
	log.Println("  按 Ctrl+C 停止服务")
	log.Println("========================================")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Printf("收到退出信号: %v，正在停止服务...\n", sig)

	proxyServer.Stop()
	_ = apiServer.Stop()
	log.Println("proxypool 已停止")
}

func getConfDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

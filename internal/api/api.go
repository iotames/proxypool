// Package api 提供 Web 管理接口。
// 接口遵循 RESTful 风格，返回 JSON。
package api

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"sync"

	"github.com/iotames/proxypool/internal/config"
	"github.com/iotames/proxypool/internal/health"
	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
	"github.com/iotames/proxypool/internal/proxy"
)

//go:embed web/*
var webFS embed.FS

// Server Web API 服务器。
type Server struct {
	cfg        *config.Conf
	pool       *pool.Pool
	server     *http.Server
	mu         sync.Mutex
	running    bool
	starting   bool // 启动流程进行中（测速/监听阶段）
	runID      int  // 启动批次号，用于识别并丢弃被停止操作取代的启动结果
	checking   bool // 手动巡检进行中
	stopCh     chan struct{}
	progressCh chan string
	proxy      *proxy.Server   // 当前代理服务实例（运行中时持有，停止时可释放端口）
	health     *health.Checker // 当前健康检查器（停止时一并停止）
	nodesMu    sync.RWMutex
	allNodes   []*node.Node // 全部已解析节点（含不可用节点），用于列表与统计
}

// NewServer 创建 Web API 服务器。
func NewServer(cfg *config.Conf, p *pool.Pool) *Server {
	return &Server{
		cfg:  cfg,
		pool: p,
	}
}

// SetAllNodes 设置全部已解析节点（含不可用节点）。
func (s *Server) SetAllNodes(nodes []*node.Node) {
	s.nodesMu.Lock()
	defer s.nodesMu.Unlock()
	s.allNodes = nodes
}

// allNodesSnapshot 返回全部节点的副本，避免与测速过程并发读写。
func (s *Server) allNodesSnapshot() []*node.Node {
	s.nodesMu.RLock()
	defer s.nodesMu.RUnlock()

	result := make([]*node.Node, len(s.allNodes))
	for i, n := range s.allNodes {
		result[i] = n.Clone()
	}
	return result
}

// SetProxy 设置代理服务器引用（CLI 模式用）。
func (s *Server) SetProxy(p *proxy.Server) {
	s.proxy = p
}

// SetHealth 设置健康检查器引用（CLI 模式用）。
// 手动巡检判定为不可用的节点需加入该检查器的待恢复列表，才能被后台重新测活。
func (s *Server) SetHealth(h *health.Checker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health = h
}

// Start 在指定地址启动 HTTP API 服务。
// isCLI: 是否为命令行模式（直接启动代理，不控制启停）。
func (s *Server) Start(addr string, isCLI bool) error {
	mux := http.NewServeMux()

	// API 路由
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/nodes", s.handleNodes)
	mux.HandleFunc("/api/health/check", s.handleHealthCheck)

	// Web 控制台模式才有的端点
	if !isCLI {
		mux.HandleFunc("/api/start", s.handleStart)
		mux.HandleFunc("/api/stop", s.handleStop)
		mux.HandleFunc("/api/progress", s.handleProgress)
	}

	// 前端静态文件
	subFS, _ := fs.Sub(webFS, "web")
	fileServer := http.FileServer(http.FS(subFS))
	mux.Handle("/", fileServer)

	s.server = &http.Server{Addr: addr, Handler: mux}

	if isCLI {
		s.mu.Lock()
		s.running = true
		s.mu.Unlock()
	}

	log.Printf("Web 管理界面已启动: http://%s\n", addr)
	return s.server.ListenAndServe()
}

// Stop 停止 API 服务器。
func (s *Server) Stop() error {
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}

// IsRunning 返回代理是否在运行。
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// OnProxyStarted 标记代理已启动（CLI 模式用）。
func (s *Server) OnProxyStarted() {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
}

// startProxy 启动代理（Web 模式用）。
// 幂等：已运行或正在启动时重复调用不产生新动作。
func (s *Server) startProxy(confDir string) error {
	s.mu.Lock()
	if s.running || s.starting {
		s.mu.Unlock()
		return nil
	}
	s.starting = true
	s.runID++
	runID := s.runID
	progressCh := make(chan string, 100)
	s.progressCh = progressCh
	stopCh := make(chan struct{})
	s.stopCh = stopCh
	s.mu.Unlock()

	// 在 goroutine 中异步执行初始化
	go func() {
		defer close(progressCh)
		result, err := runProxy(confDir, s.cfg, s.pool, progressCh, stopCh)

		s.mu.Lock()
		if s.runID != runID {
			// 本次启动已被停止操作取代，释放可能已创建的服务与检查器
			s.mu.Unlock()
			releaseRunResult(result)
			return
		}
		s.starting = false

		var nodes []*node.Node
		if result != nil {
			nodes = result.Nodes
		}

		switch {
		case errors.Is(err, ErrStartCancelled):
			log.Println("代理启动已取消")
		case err != nil:
			log.Printf("代理启动失败: %v\n", err)
		default:
			s.running = true
			s.proxy = result.Proxy
			s.health = result.Health
		}
		s.mu.Unlock()

		// 测速后记录全量节点，无论启动是否成功，前端都能看到真实状态
		s.SetAllNodes(nodes)
	}()

	return nil
}

// releaseRunResult 释放在启动被取消后仍可能创建的服务与检查器。
func releaseRunResult(result *RunResult) {
	if result == nil {
		return
	}
	if result.Proxy != nil {
		result.Proxy.Stop()
	}
	if result.Health != nil {
		result.Health.Stop()
	}
}

// stopProxy 停止代理（Web 模式用）。
// 幂等：重复调用无副作用；会真正关闭代理监听并停止健康检查。
func (s *Server) stopProxy() {
	s.mu.Lock()
	if !s.running && !s.starting {
		s.mu.Unlock()
		return
	}
	// 使正在进行的启动结果失效，避免其随后把状态改回运行中
	s.runID++
	s.starting = false
	s.running = false
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}
	proxyServer := s.proxy
	s.proxy = nil
	healthChecker := s.health
	s.health = nil
	s.mu.Unlock()

	if proxyServer != nil {
		proxyServer.Stop()
	}
	if healthChecker != nil {
		healthChecker.Stop()
	}
	// 清空池，避免停止后概况仍显示旧的可用节点
	s.pool.Clear()
	log.Println("代理已停止，端口已释放")
}

// handleStatus 返回代理池状态。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	running := s.running
	starting := s.starting
	s.mu.Unlock()

	allNodes := s.allNodesSnapshot()
	totalNodes := len(allNodes)
	availableNodes := s.pool.AvailableCount()
	if totalNodes == 0 {
		// 尚未启动代理时退回代理池数量
		totalNodes = s.pool.Size()
		availableNodes = totalNodes
	}

	status := map[string]interface{}{
		"running":        running,
		"starting":       starting,
		"totalNodes":     totalNodes,
		"availableNodes": availableNodes,
		"avgLatency":     s.pool.AvgLatency(),
	}
	s.jsonResponse(w, status)
}

// handleStart 启动隧道代理。
func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	confDir := r.URL.Query().Get("confDir")
	if confDir == "" {
		confDir = "."
	}

	if err := s.startProxy(confDir); err != nil {
		s.jsonResponse(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	s.jsonResponse(w, map[string]interface{}{"success": true})
}

// handleStop 停止隧道代理。
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.stopProxy()
	s.jsonResponse(w, map[string]interface{}{"success": true})
}

// handleHealthCheck 手动触发一次全量巡检：重测所有节点、更新节点状态并重排代理池。
// 仅在代理运行中可用；同一时间只允许一个巡检任务。
func (s *Server) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		s.jsonResponse(w, map[string]interface{}{"success": false, "error": "代理未运行，无法巡检"})
		return
	}
	if s.starting {
		s.mu.Unlock()
		s.jsonResponse(w, map[string]interface{}{"success": false, "error": "启动进行中，请稍后再试"})
		return
	}
	if s.checking {
		s.mu.Unlock()
		s.jsonResponse(w, map[string]interface{}{"success": false, "error": "巡检进行中，请稍后再试"})
		return
	}
	s.checking = true
	runID := s.runID
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.checking = false
		s.mu.Unlock()
	}()

	// 持有 nodesMu 直到测速结束，避免节点状态被并发读取时出现数据竞争
	s.nodesMu.Lock()
	nodes := make([]*node.Node, len(s.allNodes))
	copy(nodes, s.allNodes)
	total, available := health.RecheckAll(s.pool, nodes, s.cfg.GoogleTimeout)
	s.nodesMu.Unlock()

	// 巡检期间可能已被停止或重新启动，丢弃本次结果并清空池
	s.mu.Lock()
	runChanged := s.runID != runID
	checker := s.health
	s.mu.Unlock()
	if runChanged {
		s.pool.Clear()
		s.jsonResponse(w, map[string]interface{}{"success": false, "error": "代理已停止或重启，巡检结果已丢弃"})
		return
	}

	// 将巡检新判定为不可用的节点补充进后台恢复列表。
	// 后台健康检查只重测该列表内的节点，遗漏会导致节点永久移出代理池。
	if checker != nil {
		for _, n := range nodes {
			if n.Status != "available" {
				checker.AddUnavailable(n)
			}
		}
	}

	// 固定/快速模式：原固定节点仍可用则保持不变；已失效则切换到当前最快节点
	if name := s.pool.PinnedName(); name != "" {
		if s.pool.GetByName(name) != nil {
			s.pool.PinFastest(name)
		} else {
			s.pool.ReselectFastest()
		}
	}

	log.Printf("手动巡检完成：可用节点 %d / %d\n", available, total)
	s.jsonResponse(w, map[string]interface{}{
		"success":    true,
		"total":      total,
		"available":  available,
		"avgLatency": s.pool.AvgLatency(),
	})
}

// handleProgress 返回启动进度（短轮询）。
func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	messages := make([]string, 0)
	s.mu.Lock()
	ch := s.progressCh
	s.mu.Unlock()

	if ch != nil {
	drain:
		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					messages = append(messages, "__done__")
					s.mu.Lock()
					if s.progressCh == ch {
						s.progressCh = nil
					}
					s.mu.Unlock()
					break drain
				}
				messages = append(messages, msg)
			default:
				break drain
			}
		}
	}
	s.jsonResponse(w, messages)
}

// handleConfig 获取或更新配置。
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.jsonResponse(w, map[string]interface{}{
			"GOOGLE_TIMEOUT":  s.cfg.GoogleTimeout,
			"GOOGLE_TEST_URL": s.cfg.TestURL,
			"HEALTH_INTERVAL": s.cfg.HealthInterval,
			"POOL_MAX_SIZE":   s.cfg.PoolMaxSize,
			"BIND_ADDRESS":    s.cfg.BindAddress,
			"PORT":            s.cfg.Port,
			"CONF_PATH":       s.cfg.ConfPath,
		})

	case http.MethodPut:
		var updates map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if v, ok := updates["GOOGLE_TIMEOUT"]; ok {
			if f, ok := v.(float64); ok {
				s.cfg.GoogleTimeout = int(f)
			}
		}
		if v, ok := updates["HEALTH_INTERVAL"]; ok {
			if f, ok := v.(float64); ok {
				s.cfg.HealthInterval = int(f)
			}
		}
		if v, ok := updates["POOL_MAX_SIZE"]; ok {
			if f, ok := v.(float64); ok {
				s.cfg.PoolMaxSize = int(f)
			}
		}
		if v, ok := updates["BIND_ADDRESS"]; ok {
			if str, ok := v.(string); ok {
				s.cfg.BindAddress = str
			}
		}
		if v, ok := updates["PORT"]; ok {
			if f, ok := v.(float64); ok {
				s.cfg.Port = int(f)
			}
		}
		if v, ok := updates["CONF_PATH"]; ok {
			if str, ok := v.(string); ok {
				s.cfg.ConfPath = str
			}
		}
		if err := s.cfg.Save(); err != nil {
			s.jsonResponse(w, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		s.jsonResponse(w, map[string]interface{}{"success": true})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleNodes 获取节点列表。
// 返回全部已解析节点（含不可用节点），status 由测试结果决定：
// 延迟超过 GOOGLE_TIMEOUT 的节点状态为 unavailable，不计入可用池。
func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	nodes := s.allNodesSnapshot()
	if len(nodes) == 0 {
		// 尚未启动代理时退回代理池中的节点
		nodes = s.pool.All()
	}
	// 延迟升序排列，未测或延迟未知的节点排在最后
	sort.SliceStable(nodes, func(i, j int) bool {
		li, lj := nodes[i].Latency, nodes[j].Latency
		if li < 0 {
			return false
		}
		if lj < 0 {
			return true
		}
		return li < lj
	})

	nodeList := make([]map[string]interface{}, 0, len(nodes))
	for _, n := range nodes {
		nodeList = append(nodeList, map[string]interface{}{
			"name":    n.Name,
			"type":    n.Type,
			"host":    n.Server,
			"port":    n.Port,
			"latency": n.Latency,
			"status":  n.Status,
		})
	}
	s.jsonResponse(w, nodeList)
}

// jsonResponse 发送 JSON 响应。
func (s *Server) jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(data)
}

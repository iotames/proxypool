// Package api 提供 Web 管理接口。
// 接口遵循 RESTful 风格，返回 JSON。
package api

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"sync"

	"github.com/iotames/proxypool/internal/config"
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
	proxy      *proxy.Server
	mu         sync.Mutex
	running    bool
	stopCh     chan struct{}
	progressCh chan string
}

// NewServer 创建 Web API 服务器。
func NewServer(cfg *config.Conf, p *pool.Pool) *Server {
	return &Server{
		cfg:  cfg,
		pool: p,
	}
}

// SetProxy 设置代理服务器引用（CLI 模式用）。
func (s *Server) SetProxy(p *proxy.Server) {
	s.proxy = p
}

// Start 在指定地址启动 HTTP API 服务。
// isCLI: 是否为命令行模式（直接启动代理，不控制启停）。
func (s *Server) Start(addr string, isCLI bool) error {
	mux := http.NewServeMux()

	// API 路由
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/nodes", s.handleNodes)

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
		s.running = true
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
func (s *Server) startProxy(confDir string) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	progressCh := make(chan string, 100)
	s.progressCh = progressCh
	s.stopCh = make(chan struct{})

	// 在 goroutine 中异步执行初始化
	go func() {
		defer close(progressCh)
		err := runProxy(confDir, s.cfg, s.pool, progressCh, s.stopCh)
		if err != nil {
			log.Printf("代理启动失败: %v\n", err)
			return
		}
		s.mu.Lock()
		s.running = true
		s.mu.Unlock()
	}()

	return nil
}

// stopProxy 停止代理（Web 模式用）。
func (s *Server) stopProxy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	if s.stopCh != nil {
		close(s.stopCh)
	}
	s.running = false
}

// handleStatus 返回代理池状态。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	running := s.running
	s.mu.Unlock()

	status := map[string]interface{}{
		"running":        running,
		"totalNodes":     s.pool.Size(),
		"availableNodes": s.pool.AvailableCount(),
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

// handleProgress 返回启动进度（短轮询）。
func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	messages := make([]string, 0)
	if s.progressCh != nil {
		for {
			select {
			case msg, ok := <-s.progressCh:
				if !ok {
					messages = append(messages, "__done__")
					s.progressCh = nil
					break
				}
				messages = append(messages, msg)
			default:
				break
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
func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	nodes := s.pool.All()
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

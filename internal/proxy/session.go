package proxy

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
)

// 会话键最大长度，防止畸形请求撑大内存。
const sessionKeyMaxLen = 128

// sessionEntry 一个粘性会话的绑定关系。
type sessionEntry struct {
	node     *node.Node // 绑定的节点（指向池内对象，可感知健康状态变化）
	lastUsed time.Time  // 最近一次使用时间，用于空闲过期
}

// sessionTable 会话键到节点的映射表。
// 同一会话键在 TTL 内始终复用同一节点，保证出口 IP 稳定。
// 节点被移出代理池（判定不可用）或会话超时后，下次请求重新选取节点。
type sessionTable struct {
	mu        sync.Mutex
	entries   map[string]*sessionEntry
	ttl       time.Duration // 空闲过期时长，<=0 表示不过期
	maxSize   int           // 最大会话数，超出时淘汰最久未使用的会话
	storePath string        // 持久化文件路径，空表示不持久化
	loaded    bool
}

// newSessionTable 创建会话表。
// ttlSeconds: 空闲过期秒数，<=0 表示不过期。
func newSessionTable(ttlSeconds int) *sessionTable {
	return &sessionTable{
		entries: make(map[string]*sessionEntry),
		ttl:     time.Duration(ttlSeconds) * time.Second,
		maxSize: 4096,
	}
}

// SetStorePath 设置会话绑定的持久化文件。
// 代理重启后同一会话键仍会优先复用原节点，避免出口 IP 变化导致已绑定的 cookie 失效。
func (t *sessionTable) SetStorePath(path string) {
	t.mu.Lock()
	t.storePath = path
	t.loaded = false
	t.mu.Unlock()
}

// persistedSession 持久化格式。
type persistedSession struct {
	Node     string `json:"node"`
	LastUsed int64  `json:"last_used"`
}

// loadLocked 首次使用时从磁盘恢复绑定关系。调用方必须持有 t.mu。
func (t *sessionTable) loadLocked() {
	if t.loaded || t.storePath == "" {
		return
	}
	t.loaded = true
	data, err := os.ReadFile(t.storePath)
	if err != nil {
		return
	}
	var items map[string]persistedSession
	if json.Unmarshal(data, &items) != nil {
		return
	}
	for k, v := range items {
		t.entries[k] = &sessionEntry{
			node:     &node.Node{Name: v.Node},
			lastUsed: time.Unix(v.LastUsed, 0),
		}
	}
}

// saveLocked 原子写回绑定关系。调用方必须持有 t.mu。
func (t *sessionTable) saveLocked() {
	if t.storePath == "" {
		return
	}
	items := make(map[string]persistedSession, len(t.entries))
	for k, e := range t.entries {
		items[k] = persistedSession{Node: e.node.Name, LastUsed: e.lastUsed.Unix()}
	}
	data, err := json.Marshal(items)
	if err != nil {
		return
	}
	tmp := t.storePath + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, t.storePath)
	}
}

// Get 返回会话键绑定的节点，必要时新建绑定。
// 返回值 reused 表示本次是否复用了已有绑定；新建时 node 由 pick 选出。
// pick 为 nil 或返回 nil 时，Get 返回 nil。
func (t *sessionTable) Get(key string, p *pool.Pool, pick func() *node.Node) (n *node.Node, reused bool) {
	if key == "" || pick == nil {
		return nil, false
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.loadLocked()

	now := time.Now()
	if e, ok := t.entries[key]; ok {
		expired := t.ttl > 0 && now.Sub(e.lastUsed) > t.ttl
		// 节点被健康检查移出代理池后，旧绑定立即失效
		if !expired && p.Contains(e.node.Name) {
			if live := p.GetByName(e.node.Name); live != nil {
				e.node = live
				e.lastUsed = now
				return live, true
			}
		}
		delete(t.entries, key)
		t.saveLocked()
	}

	n = pick()
	if n == nil {
		return nil, false
	}
	t.entries[key] = &sessionEntry{node: n, lastUsed: now}
	t.evictLocked(now)
	t.saveLocked()
	return n, false
}

// Invalidate 解除指定会话的绑定。
func (t *sessionTable) Invalidate(key string) {
	t.mu.Lock()
	delete(t.entries, key)
	t.mu.Unlock()
}

// Count 返回当前会话数量（含已过期但尚未清理的条目）。
func (t *sessionTable) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// evictLocked 清理过期会话，并在超出容量时淘汰最久未使用的会话。
// 调用方必须持有 t.mu。
func (t *sessionTable) evictLocked(now time.Time) {
	if t.ttl > 0 {
		for k, e := range t.entries {
			if now.Sub(e.lastUsed) > t.ttl {
				delete(t.entries, k)
			}
		}
	}
	if len(t.entries) <= t.maxSize {
		return
	}
	oldestKey := ""
	var oldest time.Time
	for k, e := range t.entries {
		if oldestKey == "" || e.lastUsed.Before(oldest) {
			oldestKey, oldest = k, e.lastUsed
		}
	}
	if oldestKey != "" {
		delete(t.entries, oldestKey)
	}
}

// sessionKeyFromAuth 从 Proxy-Authorization 头解析会话键。
// 支持两种形式：
//   - Basic base64(user:pass)  -> 会话键为 "user"（pass 为空）或 "user:pass"
//   - Bearer <token>           -> 会话键为 token
//
// 无法识别时返回空字符串，此时按普通随机模式处理。
func sessionKeyFromAuth(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}

	scheme, value, ok := strings.Cut(header, " ")
	if !ok {
		return ""
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	var key string
	switch {
	case strings.EqualFold(scheme, "Basic"):
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return ""
		}
		key = string(raw)
		// 约定 "会话名:" 与 "会话名" 等价，便于客户端只填用户名
		key = strings.TrimSuffix(key, ":")
	case strings.EqualFold(scheme, "Bearer"):
		key = value
	default:
		return ""
	}

	if len(key) > sessionKeyMaxLen {
		key = key[:sessionKeyMaxLen]
	}
	return key
}

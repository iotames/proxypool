// Package node 定义代理节点模型。
// 节点从 Clash 配置中提取，支持 trojan 和 ss 两种类型。
package node

import (
	"fmt"
	"net"
	"sync"
)

// Node 表示一个代理节点。
// 对应 Clash 配置中 proxies 数组的一个条目。
type Node struct {
	Name           string // 节点名称，如 "🇭🇰 香港 01 Pro"
	Type           string // 节点类型：trojan 或 ss
	Server         string // 服务器地址（域名或 IP）
	Port           int    // 服务器端口
	Password       string // 密码
	Cipher         string // 加密方式（仅 ss 类型使用）
	SNI            string // TLS SNI（仅 trojan 类型使用）
	SkipCertVerify bool   // 是否跳过证书验证（仅 trojan 类型使用）
	UDP            bool   // 是否支持 UDP

	// 运行时状态：latency 与 status 必须作为整体原子读写，
	// 否则测速 goroutine 与快照读取之间会出现
	// “状态已置 unavailable 但延迟仍是上一轮旧值”的撕裂视图。
	stateMu sync.Mutex
	latency int64  // 延迟（毫秒），-1 表示未测试或不可达
	status  string // untested / testing / available / unavailable
}

// Addr 返回节点的 server:port 格式地址。
func (n *Node) Addr() string {
	return net.JoinHostPort(n.Server, fmt.Sprintf("%d", n.Port))
}

// SetState 原子地设置延迟与状态（一次锁内同时更新两个字段）。
func (n *Node) SetState(latency int64, status string) {
	n.stateMu.Lock()
	n.latency = latency
	n.status = status
	n.stateMu.Unlock()
}

// SetStatus 只更新状态，保留当前延迟值（用于 testing 等过渡态）。
func (n *Node) SetStatus(status string) {
	n.stateMu.Lock()
	n.status = status
	n.stateMu.Unlock()
}

// GetState 原子地读取延迟与状态的一致快照。
func (n *Node) GetState() (int64, string) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.latency, n.status
}

// Latency 返回当前延迟（毫秒）。
func (n *Node) Latency() int64 {
	l, _ := n.GetState()
	return l
}

// Status 返回当前状态字符串。
func (n *Node) Status() string {
	_, s := n.GetState()
	return s
}

// Clone 返回节点副本；状态字段在锁内一次性读取，保证与源节点当前状态一致。
// 副本独立持有状态，供无并发争用的展示/序列化使用。
func (n *Node) Clone() *Node {
	latency, status := n.GetState()
	return &Node{
		Name:           n.Name,
		Type:           n.Type,
		Server:         n.Server,
		Port:           n.Port,
		Password:       n.Password,
		Cipher:         n.Cipher,
		SNI:            n.SNI,
		SkipCertVerify: n.SkipCertVerify,
		UDP:            n.UDP,
		latency:        latency,
		status:         status,
	}
}

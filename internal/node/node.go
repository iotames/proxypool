// Package node 定义代理节点模型。
// 节点从 Clash 配置中提取，支持 trojan 和 ss 两种类型。
package node

import (
	"fmt"
	"net"
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

	// 运行时状态
	Latency int64  // 延迟（毫秒），-1 表示未测试或不可用
	Status  string // 节点状态：untested / available / unavailable / testing
}

// Addr 返回节点的 server:port 格式地址。
func (n *Node) Addr() string {
	return net.JoinHostPort(n.Server, fmt.Sprintf("%d", n.Port))
}

// Clone 返回节点的浅拷贝副本。
func (n *Node) Clone() *Node {
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
		Latency:        n.Latency,
		Status:         n.Status,
	}
}

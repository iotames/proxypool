// Package clash 解析 Clash 配置文件。
// 支持从本地 YAML 文件和远程 proxy-providers 订阅中提取节点。
//
// 使用方式：
//
//	nodes, err := clash.ParseFile("clash.yaml")
//	if err != nil { ... }
//	for _, node := range nodes {
//	    fmt.Printf("节点: %s (%s)\n", node.Name, node.Type)
//	}
package clash

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/iotames/proxypool/internal/node"
	"gopkg.in/yaml.v3"
)

// clashProxy 对应 Clash 配置中 proxies 数组的单个条目。
type clashProxy struct {
	Name           string `yaml:"name"`
	Type           string `yaml:"type"`
	Server         string `yaml:"server"`
	Port           int    `yaml:"port"`
	Password       string `yaml:"password"`
	Cipher         string `yaml:"cipher"`
	SNI            string `yaml:"sni"`
	SkipCertVerify bool   `yaml:"skip-cert-verify"`
	UDP            bool   `yaml:"udp"`
}

// clashProvider 对应 Clash 配置中 proxy-providers 的单个条目。
type clashProvider struct {
	Type string `yaml:"type"`
	URL  string `yaml:"url"`
	Path string `yaml:"path"`
}

// clashConfig 是 Clash YAML 配置文件的顶级结构。
type clashConfig struct {
	Proxies        []clashProxy             `yaml:"proxies"`
	ProxyProviders map[string]clashProvider `yaml:"proxy-providers"`
}

// ParseFile 解析本地 Clash YAML 配置文件，提取 ss 和 trojan 类型节点。
// 如果配置中包含 proxy-providers 远程订阅，也会自动拉取解析。
//
// filepath: Clash 配置文件路径。
// 返回提取到的节点列表和可能的错误。
func ParseFile(filepath string) ([]*node.Node, error) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("读取 Clash 配置文件(%s)失败: %w", filepath, err)
	}

	var cfg clashConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析 Clash YAML 失败: %w", err)
	}

	var nodes []*node.Node

	// 解析本地 proxies
	for _, p := range cfg.Proxies {
		n := proxyToNode(p)
		if n != nil {
			nodes = append(nodes, n)
		}
	}

	// 解析远程 proxy-providers
	for name, provider := range cfg.ProxyProviders {
		providerNodes, err := parseProvider(name, provider)
		if err != nil {
			fmt.Printf("警告: 拉取 proxy-provider(%s) 失败: %v\n", name, err)
			continue
		}
		nodes = append(nodes, providerNodes...)
	}

	return nodes, nil
}

// proxyToNode 将 Clash proxy 条目转换为 Node。
// 仅支持 trojan 和 ss 类型，其他类型返回 nil。
func proxyToNode(p clashProxy) *node.Node {
	switch p.Type {
	case "trojan", "ss":
		// 有效类型
	default:
		return nil
	}

	return &node.Node{
		Name:           p.Name,
		Type:           p.Type,
		Server:         p.Server,
		Port:           p.Port,
		Password:       p.Password,
		Cipher:         p.Cipher,
		SNI:            p.SNI,
		SkipCertVerify: p.SkipCertVerify,
		UDP:            p.UDP,
		Status:         "untested",
		Latency:        -1,
	}
}

// parseProvider 拉取并解析远程 proxy-provider。
// 支持 type=http 的远程订阅，订阅内容应为 proxies 数组或 ss:///trojan:// URI 列表。
func parseProvider(name string, provider clashProvider) ([]*node.Node, error) {
	if provider.Type != "http" {
		return nil, fmt.Errorf("不支持的 provider 类型: %s", provider.Type)
	}
	if provider.URL == "" {
		return nil, fmt.Errorf("proxy-provider(%s) 的 URL 为空", name)
	}

	// 拉取远程订阅内容
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(provider.URL)
	if err != nil {
		return nil, fmt.Errorf("拉取订阅失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取订阅响应失败: %w", err)
	}

	content := string(body)

	// 尝试按 YAML proxies 数组解析
	var proxies []clashProxy
	if err := yaml.Unmarshal(body, &proxies); err == nil {
		var nodes []*node.Node
		for _, p := range proxies {
			if n := proxyToNode(p); n != nil {
				nodes = append(nodes, n)
			}
		}
		if len(nodes) > 0 {
			return nodes, nil
		}
	}

	// 尝试按 YAML 对象格式解析（带 proxies 字段）
	var cfg clashConfig
	if err := yaml.Unmarshal(body, &cfg); err == nil && len(cfg.Proxies) > 0 {
		var nodes []*node.Node
		for _, p := range cfg.Proxies {
			if n := proxyToNode(p); n != nil {
				nodes = append(nodes, n)
			}
		}
		return nodes, nil
	}

	// 尝试按 URI 列表解析（每行一个 ss:// 或 trojan:// URI）
	var nodes []*node.Node
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n, err := parseURI(line)
		if err != nil {
			fmt.Printf("警告: 解析 URI(%s) 失败: %v\n", line[:min(len(line), 60)], err)
			continue
		}
		if n != nil {
			nodes = append(nodes, n)
		}
	}

	return nodes, nil
}

// parseURI 解析 ss:// 或 trojan:// URI 为节点。
func parseURI(uriStr string) (*node.Node, error) {
	u, err := url.Parse(uriStr)
	if err != nil {
		return nil, err
	}

	switch u.Scheme {
	case "trojan":
		return parseTrojanURI(u)
	case "ss":
		return parseSSURI(u)
	default:
		return nil, fmt.Errorf("不支持的 URI scheme: %s", u.Scheme)
	}
}

// parseTrojanURI 解析 trojan:// URI。
// 格式: trojan://password@server:port?allowInsecure=1&peer=sni#name
func parseTrojanURI(u *url.URL) (*node.Node, error) {
	password := u.User.String()
	host := u.Hostname()
	portStr := u.Port()
	if portStr == "" {
		portStr = "443"
	}

	n := &node.Node{
		Name:     u.Fragment,
		Type:     "trojan",
		Server:   host,
		Password: password,
		Status:   "untested",
		Latency:  -1,
	}
	fmt.Sscanf(portStr, "%d", &n.Port)

	q := u.Query()
	if peer := q.Get("peer"); peer != "" {
		n.SNI = peer
	}
	if q.Get("allowInsecure") == "1" || q.Get("allowInsecure") == "true" {
		n.SkipCertVerify = true
	}

	if n.Name == "" {
		n.Name = fmt.Sprintf("trojan-%s:%d", n.Server, n.Port)
	}

	return n, nil
}

// parseSSURI 解析 ss:// URI。
// 支持的格式:
//   - 传统格式: ss://method:password@server:port#name
//   - SIP002 格式: ss://base64(method:password)@server:port#name
func parseSSURI(u *url.URL) (*node.Node, error) {
	userInfo := u.User.String()

	var method, password string
	if idx := strings.Index(userInfo, ":"); idx > 0 {
		// 传统格式 method:password
		method = userInfo[:idx]
		password = userInfo[idx+1:]
	} else {
		// SIP002 base64 编码
		decoded, err := decodeBase64(userInfo)
		if err != nil {
			return nil, fmt.Errorf("ss URI 解析失败: %w", err)
		}
		if idx := strings.Index(decoded, ":"); idx > 0 {
			method = decoded[:idx]
			password = decoded[idx+1:]
		}
	}

	host := u.Hostname()
	portStr := u.Port()
	if portStr == "" {
		portStr = "443"
	}

	n := &node.Node{
		Name:     u.Fragment,
		Type:     "ss",
		Server:   host,
		Password: password,
		Cipher:   method,
		Status:   "untested",
		Latency:  -1,
	}
	fmt.Sscanf(portStr, "%d", &n.Port)

	if n.Name == "" {
		n.Name = fmt.Sprintf("ss-%s:%d", n.Server, n.Port)
	}

	return n, nil
}

// decodeBase64 解码 base64 字符串（兼容 Clash 的 base64 编码，无 padding 也可）。
func decodeBase64(s string) (string, error) {
	// 添加 padding
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(s)
		if err != nil {
			return "", err
		}
	}
	return string(decoded), nil
}

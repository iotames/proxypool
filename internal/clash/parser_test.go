package clash

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseLocalYAML(t *testing.T) {
	// 创建临时 Clash 配置文件
	content := `
proxies:
  - name: "测试节点1"
    type: trojan
    server: 1.2.3.4
    port: 443
    password: testpass123
    udp: true
    sni: example.com
    skip-cert-verify: true

  - name: "测试节点2"
    type: ss
    server: 5.6.7.8
    port: 8388
    cipher: aes-256-gcm
    password: sspass456
    udp: false

  - name: "不支持的节点"
    type: vmess
    server: 9.10.11.12
    port: 10086
`
	dir := t.TempDir()
	fpath := filepath.Join(dir, "clash.yaml")
	if err := os.WriteFile(fpath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	nodes, err := ParseFile(fpath)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if len(nodes) != 2 {
		t.Fatalf("期望 2 个节点（trojan+ss），实际得到 %d", len(nodes))
	}

	// 验证第一个 trojan 节点
	n1 := nodes[0]
	if n1.Name != "测试节点1" {
		t.Errorf("节点名期望='测试节点1', 实际='%s'", n1.Name)
	}
	if n1.Type != "trojan" {
		t.Errorf("类型期望='trojan', 实际='%s'", n1.Type)
	}
	if n1.Server != "1.2.3.4" {
		t.Errorf("服务器期望='1.2.3.4', 实际='%s'", n1.Server)
	}
	if n1.Port != 443 {
		t.Errorf("端口期望=443, 实际=%d", n1.Port)
	}
	if n1.Password != "testpass123" {
		t.Errorf("密码期望='testpass123', 实际='%s'", n1.Password)
	}
	if !n1.SkipCertVerify {
		t.Error("SkipCertVerify 期望=true, 实际=false")
	}

	// 验证第二个 ss 节点
	n2 := nodes[1]
	if n2.Name != "测试节点2" {
		t.Errorf("节点名期望='测试节点2', 实际='%s'", n2.Name)
	}
	if n2.Type != "ss" {
		t.Errorf("类型期望='ss', 实际='%s'", n2.Type)
	}
	if n2.Cipher != "aes-256-gcm" {
		t.Errorf("加密方式期望='aes-256-gcm', 实际='%s'", n2.Cipher)
	}
}

func TestParseProviderNotSupported(t *testing.T) {
	// 测试不支持的文件不存在场景
	_, err := ParseFile("not_exists.yaml")
	if err == nil {
		t.Fatal("不存在的文件应该报错")
	}
}

func TestProxyToNode(t *testing.T) {
	// 测试 vmess 节点被过滤
	p := clashProxy{
		Name:   "vmess节点",
		Type:   "vmess",
		Server: "1.2.3.4",
		Port:   443,
	}
	n := proxyToNode(p)
	if n != nil {
		t.Fatal("vmess 类型应返回 nil")
	}

	// 测试 ss 节点
	p2 := clashProxy{
		Name:     "ss节点",
		Type:     "ss",
		Server:   "5.6.7.8",
		Port:     8388,
		Password: "sspass",
		Cipher:   "chacha20-ietf-poly1305",
	}
	n2 := proxyToNode(p2)
	if n2 == nil {
		t.Fatal("ss 类型应返回节点")
	}
	if n2.Type != "ss" || n2.Cipher != "chacha20-ietf-poly1305" {
		t.Error("ss 节点属性不正确")
	}
}

func TestClashConfigLoadYAML(t *testing.T) {
	// 验证 YAML 解析能正确读取 clash 配置结构
	yamlContent := `
proxies:
  - { name: 'test1', type: trojan, server: a.com, port: 443, password: pass1, udp: true, sni: b.com, skip-cert-verify: true }
proxy-providers:
  provider1:
    type: http
    url: "https://example.com/sub"
    path: ./proxy_provider1.yaml
`
	var cfg clashConfig
	if err := yaml.Unmarshal([]byte(yamlContent), &cfg); err != nil {
		t.Fatalf("YAML 解析失败: %v", err)
	}
	if len(cfg.Proxies) != 1 {
		t.Fatalf("期望 1 个 proxy, 实际 %d", len(cfg.Proxies))
	}
	if cfg.Proxies[0].Name != "test1" {
		t.Errorf("名称解析错误: %s", cfg.Proxies[0].Name)
	}
	if cfg.Proxies[0].SkipCertVerify != true {
		t.Error("skip-cert-verify 应解析为 true")
	}
	if len(cfg.ProxyProviders) != 1 {
		t.Fatalf("期望 1 个 provider, 实际 %d", len(cfg.ProxyProviders))
	}
}

package config

import (
	"os"
	"testing"
)

func TestLoadConfigWithDefaults(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(dir+"/.env", []byte{}, 0644)
	os.WriteFile(dir+"/default.env", []byte{}, 0644)

	cfg, err := Load(dir, false)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	if cfg.Port != 1080 {
		t.Errorf("默认端口期望 1080, 实际 %d", cfg.Port)
	}
	if cfg.ConfPath != "clash.yaml" {
		t.Errorf("默认配置路径期望 'clash.yaml', 实际 '%s'", cfg.ConfPath)
	}
	if cfg.GoogleTimeout != 300 {
		t.Errorf("默认超时期望 300, 实际 %d", cfg.GoogleTimeout)
	}
	if cfg.TestURL != "http://www.google.com" {
		t.Errorf("默认测试URL期望 'http://www.google.com', 实际 '%s'", cfg.TestURL)
	}
	if cfg.HealthInterval != 60 {
		t.Errorf("默认健康检查间隔期望 60, 实际 %d", cfg.HealthInterval)
	}
	if cfg.PoolMaxSize != 0 {
		t.Errorf("默认池大小期望 0, 实际 %d", cfg.PoolMaxSize)
	}
	if cfg.BindAddress != "127.0.0.1" {
		t.Errorf("默认绑定地址期望 '127.0.0.1', 实际 '%s'", cfg.BindAddress)
	}
}

func TestLoadConfigFromEnvVar(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/.env", []byte("port = 9999\nGOOGLE_TIMEOUT = 500\n"), 0644)
	os.WriteFile(dir+"/default.env", []byte{}, 0644)

	cfg, err := Load(dir, false)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	if cfg.Port != 9999 {
		t.Errorf("端口期望 9999, 实际 %d", cfg.Port)
	}
	if cfg.GoogleTimeout != 500 {
		t.Errorf("超时期望 500, 实际 %d", cfg.GoogleTimeout)
	}
}

func TestAutoCreateEnvFiles(t *testing.T) {
	dir := t.TempDir()

	cfg, err := Load(dir, false)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg == nil {
		t.Fatal("配置不应为 nil")
	}

	if _, err := os.Stat(dir + "/.env"); os.IsNotExist(err) {
		t.Error(".env 文件未被自动创建")
	}
	if _, err := os.Stat(dir + "/default.env"); os.IsNotExist(err) {
		t.Error("default.env 文件未被自动创建")
	}
}

func TestApplyAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir, false)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	err = cfg.Apply(map[string]interface{}{
		"GOOGLE_TEST_URL": "https://example.com/health",
		"LATENCY_MAX":     float64(250),
		"STICKY_SESSION":  false,
	})
	if err != nil {
		t.Fatalf("应用配置失败: %v", err)
	}
	snap := cfg.Snapshot()
	if snap.TestURL != "https://example.com/health" || snap.LatencyMax != 250 || snap.StickySession {
		t.Fatalf("快照未反映更新: %+v", snap)
	}

	// 任一字段非法时整体不提交
	err = cfg.Apply(map[string]interface{}{
		"LATENCY_MAX":     float64(100),
		"HEALTH_INTERVAL": float64(0),
	})
	if err == nil {
		t.Fatal("非法间隔应报错")
	}
	if got := cfg.Snapshot().LatencyMax; got != 250 {
		t.Fatalf("非法更新不应部分提交, 实际 LATENCY_MAX=%d", got)
	}
}

package api

import (
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iotames/proxypool/internal/config"
	"github.com/iotames/proxypool/internal/pool"
)

func newTestServer(t *testing.T) (*Server, *config.Conf, string) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(dir, false)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	return NewServer(cfg, pool.New(0)), cfg, dir
}

func callConfig(t *testing.T, s *Server, method, body string) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(method, "/api/config", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleConfig(rec, req)
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, rec.Body.String())
	}
	return resp
}

func TestConfigAPIReadsAndWritesTestOptions(t *testing.T) {
	s, cfg, dir := newTestServer(t)

	got := callConfig(t, s, "GET", "")
	for _, key := range []string{"GOOGLE_TEST_URL", "GOOGLE_TIMEOUT", "LATENCY_MAX", "STICKY_SESSION"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("GET /api/config 缺少字段 %s", key)
		}
	}

	resp := callConfig(t, s, "PUT", `{"GOOGLE_TEST_URL":"https://example.com/probe","LATENCY_MAX":250,"STICKY_SESSION":false}`)
	if resp["success"] != true {
		t.Fatalf("保存配置失败: %v", resp)
	}
	if snap := cfg.Snapshot(); snap.TestURL != "https://example.com/probe" || snap.LatencyMax != 250 || snap.StickySession {
		t.Fatalf("内存配置未更新: %+v", snap)
	}

	// .env 持久化后重新加载应保持新值
	reloaded, err := config.Load(dir, false)
	if err != nil {
		t.Fatalf("重新加载配置失败: %v", err)
	}
	if snap := reloaded.Snapshot(); snap.TestURL != "https://example.com/probe" || snap.LatencyMax != 250 {
		t.Fatalf("配置未持久化: %+v", snap)
	}
}

func TestConfigAPIRejectsInvalidValues(t *testing.T) {
	s, cfg, _ := newTestServer(t)
	before := cfg.Snapshot()

	for _, body := range []string{
		`{"GOOGLE_TEST_URL":"ftp://example.com"}`,
		`{"GOOGLE_TEST_URL":""}`,
		`{"LATENCY_MAX":-1}`,
		`{"PORT":70000}`,
	} {
		resp := callConfig(t, s, "PUT", body)
		if resp["success"] != false {
			t.Fatalf("非法请求 %s 应失败: %v", body, resp)
		}
	}
	if snap := cfg.Snapshot(); snap != before {
		t.Fatalf("非法请求不应修改配置: %+v -> %+v", before, snap)
	}
}

func TestWebPageHasConfigFields(t *testing.T) {
	data, err := fs.ReadFile(webFS, "web/index.html")
	if err != nil {
		t.Fatalf("读取内嵌页面失败: %v", err)
	}
	page := string(data)
	for _, id := range []string{`id="testUrl"`, `id="latencyMax"`, `id="stickySession"`} {
		if !strings.Contains(page, id) {
			t.Fatalf("页面缺少控件 %s", id)
		}
	}
}

package node

import "testing"

func TestParseTestTarget(t *testing.T) {
	cases := []struct {
		raw    string
		scheme string
		host   string
		port   int
		path   string
		addr   string
	}{
		{"", "http", "www.google.com", 80, "/", "www.google.com:80"},
		{"http://example.com", "http", "example.com", 80, "/", "example.com:80"},
		{"https://example.com", "https", "example.com", 443, "/", "example.com:443"},
		{"HTTPS://example.com/health?x=1", "https", "example.com", 443, "/health?x=1", "example.com:443"},
		{"https://example.com:8443/probe", "https", "example.com", 8443, "/probe", "example.com:8443"},
		{"example.com:8080/path", "http", "example.com", 8080, "/path", "example.com:8080"},
	}
	for _, c := range cases {
		got, err := ParseTestTarget(c.raw)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", c.raw, err)
		}
		if got.Scheme != c.scheme || got.Host != c.host || got.Port != c.port || got.Path != c.path {
			t.Fatalf("解析 %q 结果不符: %+v", c.raw, got)
		}
		if got.Addr() != c.addr {
			t.Fatalf("解析 %q 地址期望 %q, 实际 %q", c.raw, c.addr, got.Addr())
		}
	}
}

func TestParseTestTargetRejectsInvalid(t *testing.T) {
	for _, raw := range []string{"ftp://example.com", "https://", "http://example.com:0", "http://example.com:70000"} {
		if _, err := ParseTestTarget(raw); err == nil {
			t.Fatalf("非法目标 %q 应报错", raw)
		}
	}
}

func TestNewTestOptionsDefaults(t *testing.T) {
	opts, err := NewTestOptions("", 0, -5)
	if err != nil {
		t.Fatalf("构造测试参数失败: %v", err)
	}
	if opts.Timeout != 300 || opts.LatencyMax != 0 {
		t.Fatalf("超时/上限回退不符: %+v", opts)
	}
	if opts.Target.Host != "www.google.com" || opts.Target.Port != 80 {
		t.Fatalf("默认目标不符: %+v", opts.Target)
	}
}

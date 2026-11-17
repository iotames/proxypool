package proxy

import (
	"bufio"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/iotames/proxypool/internal/node"
	"github.com/iotames/proxypool/internal/pool"
)

// newTestPool 构造一个包含指定名称节点的代理池。
func newTestPool(names ...string) *pool.Pool {
	p := pool.New(0)
	for _, name := range names {
		n := &node.Node{Name: name, Type: "trojan", Server: "127.0.0.1", Port: 1}
		n.SetState(10, "available")
		p.Add(n)
	}
	return p
}

func TestSessionKeyFromAuth(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Basic " + base64.StdEncoding.EncodeToString([]byte("sticky-1:")), "sticky-1"},
		{"basic " + base64.StdEncoding.EncodeToString([]byte("sticky-2:pass")), "sticky-2:pass"},
		{"Bearer token-abc", "token-abc"},
		{"Basic !!!not-base64!!!", ""},
		{"", ""},
		{"Basic", ""},
		{"Digest something", ""},
	}
	for _, c := range cases {
		if got := sessionKeyFromAuth(c.header); got != c.want {
			t.Errorf("sessionKeyFromAuth(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

func TestSessionKeyTruncated(t *testing.T) {
	long := strings.Repeat("a", sessionKeyMaxLen+50)
	header := "Basic " + base64.StdEncoding.EncodeToString([]byte(long))
	if got := sessionKeyFromAuth(header); len(got) != sessionKeyMaxLen {
		t.Fatalf("会话键长度 = %d, want %d", len(got), sessionKeyMaxLen)
	}
}

func TestReadHTTPRequestHeader(t *testing.T) {
	auth := base64.StdEncoding.EncodeToString([]byte("sticky-9:"))
	raw := "CONNECT www.next.co.uk:443 HTTP/1.1\r\n" +
		"Host: www.next.co.uk:443\r\n" +
		"Proxy-Authorization: Basic " + auth + "\r\n" +
		"Proxy-Connection: keep-alive\r\n" +
		"User-Agent: test\r\n" +
		"\r\n"
	br := bufio.NewReader(strings.NewReader(raw))

	hdr, err := readHTTPRequestHeader(br)
	if err != nil {
		t.Fatalf("解析请求头失败: %v", err)
	}
	if hdr.Method != "CONNECT" || hdr.Target != "www.next.co.uk:443" || hdr.Version != "HTTP/1.1" {
		t.Fatalf("请求行解析错误: %+v", hdr)
	}
	if hdr.Session != "sticky-9" {
		t.Fatalf("会话键 = %q, want sticky-9", hdr.Session)
	}
	if got := hdr.Header("user-agent"); got != "test" {
		t.Fatalf("User-Agent = %q", got)
	}
	for _, f := range hdr.Fields {
		if strings.EqualFold(f[0], "Proxy-Authorization") || strings.EqualFold(f[0], "Proxy-Connection") {
			t.Fatalf("代理专用头不应转发: %s", f[0])
		}
	}
}

func TestReadHTTPRequestHeaderTooLarge(t *testing.T) {
	big := "GET http://example.com/ HTTP/1.1\r\nX-Big: " + strings.Repeat("a", maxHTTPHeaderBytes+10) + "\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(big))
	if _, err := readHTTPRequestHeader(br); err == nil {
		t.Fatal("超长请求头应返回错误")
	}
}

func TestSessionReuse(t *testing.T) {
	p := newTestPool("a", "b")
	tbl := newSessionTable(600)

	picks := 0
	pick := func() *node.Node {
		picks++
		if picks == 1 {
			return p.GetByName("a")
		}
		return p.GetByName("b")
	}

	first, reused := tbl.Get("s1", p, pick)
	if first == nil || reused {
		t.Fatalf("首次应新建绑定: node=%v reused=%v", first, reused)
	}
	second, reused := tbl.Get("s1", p, pick)
	if second == nil || !reused {
		t.Fatalf("再次请求应复用绑定: node=%v reused=%v", second, reused)
	}
	if first.Name != second.Name || picks != 1 {
		t.Fatalf("同一会话应固定同一节点: %s vs %s, picks=%d", first.Name, second.Name, picks)
	}

	other, _ := tbl.Get("s2", p, pick)
	if other == nil || other.Name != "b" {
		t.Fatalf("不同会话应独立选路: %v", other)
	}
}

func TestSessionExpired(t *testing.T) {
	p := newTestPool("a", "b")
	tbl := newSessionTable(1)
	picks := 0
	pick := func() *node.Node {
		picks++
		if picks == 1 {
			return p.GetByName("a")
		}
		return p.GetByName("b")
	}

	tbl.Get("s1", p, pick)
	// 人为把最近使用时间前移，触发空闲过期
	tbl.mu.Lock()
	tbl.entries["s1"].lastUsed = time.Now().Add(-2 * time.Second)
	tbl.mu.Unlock()

	n, reused := tbl.Get("s1", p, pick)
	if reused {
		t.Fatal("会话过期后不应复用旧绑定")
	}
	if n == nil || n.Name != "b" {
		t.Fatalf("过期后应重新选路: %v", n)
	}
}

func TestSessionInvalidWhenNodeRemoved(t *testing.T) {
	p := newTestPool("a", "b")
	tbl := newSessionTable(600)
	picks := 0
	pick := func() *node.Node {
		picks++
		if picks == 1 {
			return p.GetByName("a")
		}
		return p.GetByName("b")
	}

	tbl.Get("s1", p, pick)
	p.Remove("a")
	n, reused := tbl.Get("s1", p, pick)
	if reused {
		t.Fatal("节点被移出代理池后不应复用旧绑定")
	}
	if n == nil || n.Name != "b" {
		t.Fatalf("应重新选路到存活节点: %v", n)
	}
}

func TestSessionTableEvict(t *testing.T) {
	p := newTestPool("a")
	tbl := newSessionTable(0)
	tbl.maxSize = 2
	for _, k := range []string{"s1", "s2", "s3"} {
		tbl.Get(k, p, p.GetRandom)
	}
	if got := tbl.Count(); got != 2 {
		t.Fatalf("超出容量时应淘汰最久未使用会话: count=%d", got)
	}
}

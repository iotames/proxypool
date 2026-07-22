package pool

import (
	"testing"

	"github.com/iotames/proxypool/internal/node"
)

func newNode(name, ntype string, latency int64) *node.Node {
	return &node.Node{
		Name:    name,
		Type:    ntype,
		Server:  "test.com",
		Port:    443,
		Latency: latency,
		Status:  "available",
	}
}

func TestPoolAddAndSize(t *testing.T) {
	p := New(0) // 不限制大小

	if p.Size() != 0 {
		t.Fatalf("新池大小应为 0, 实际 %d", p.Size())
	}

	p.Add(newNode("node1", "trojan", 100))
	if p.Size() != 1 {
		t.Fatalf("添加后大小应为 1, 实际 %d", p.Size())
	}

	p.Add(newNode("node2", "trojan", 200))
	if p.Size() != 2 {
		t.Fatalf("添加后大小应为 2, 实际 %d", p.Size())
	}
}

func TestPoolAddDuplicate(t *testing.T) {
	p := New(0)
	p.Add(newNode("node1", "trojan", 100))
	added := p.Add(newNode("node1", "trojan", 150))
	if added {
		t.Fatal("重复名称的节点不应被添加")
	}
	if p.Size() != 1 {
		t.Fatalf("大小应为 1, 实际 %d", p.Size())
	}
}

func TestPoolRemove(t *testing.T) {
	p := New(0)
	p.Add(newNode("node1", "trojan", 100))
	p.Add(newNode("node2", "trojan", 200))

	removed := p.Remove("node1")
	if !removed {
		t.Fatal("移除 node1 应成功")
	}
	if p.Size() != 1 {
		t.Fatalf("移除后大小应为 1, 实际 %d", p.Size())
	}

	removed = p.Remove("not_exist")
	if removed {
		t.Fatal("移除不存在的节点应返回 false")
	}
}

func TestPoolGetRandom(t *testing.T) {
	p := New(0)

	// 空池返回 nil
	if n := p.GetRandom(); n != nil {
		t.Fatal("空池应返回 nil")
	}

	p.Add(newNode("node1", "trojan", 100))
	n := p.GetRandom()
	if n == nil {
		t.Fatal("非空池应返回节点")
	}
	if n.Name != "node1" {
		t.Fatalf("期望 node1, 实际 %s", n.Name)
	}
}

func TestPoolAll(t *testing.T) {
	p := New(0)
	p.Add(newNode("node1", "trojan", 100))
	p.Add(newNode("node2", "ss", 200))

	all := p.All()
	if len(all) != 2 {
		t.Fatalf("期望 2 个节点, 实际 %d", len(all))
	}

	// 修改返回的副本不应影响原池
	all[0].Name = "changed"
	if p.All()[0].Name == "changed" {
		t.Fatal("All() 应返回副本，修改不应影响原池")
	}
}

func TestPoolMaxSize(t *testing.T) {
	p := New(2)

	p.Add(newNode("node1", "trojan", 100))
	p.Add(newNode("node2", "trojan", 200))
	added := p.Add(newNode("node3", "trojan", 300))

	if added {
		t.Fatal("超限时低延迟节点也应被限制")
	}
	if p.Size() != 2 {
		t.Fatalf("大小应为 2, 实际 %d", p.Size())
	}
}

func TestPoolAvgLatency(t *testing.T) {
	p := New(0)

	if avg := p.AvgLatency(); avg != 0 {
		t.Fatalf("空池平均延迟应为 0, 实际 %f", avg)
	}

	p.Add(newNode("node1", "trojan", 100))
	p.Add(newNode("node2", "trojan", 200))

	avg := p.AvgLatency()
	if avg != 150 {
		t.Fatalf("平均延迟期望 150, 实际 %f", avg)
	}
}

func TestPoolGetFastestEmpty(t *testing.T) {
	p := New(0)
	if n := p.GetFastest(); n != nil {
		t.Fatal("空池 GetFastest 应返回 nil")
	}
}

func TestPoolGetFastestReturnsLowestLatency(t *testing.T) {
	p := New(0)
	p.Add(newNode("slow", "trojan", 300))
	p.Add(newNode("fast", "trojan", 50))
	p.Add(newNode("medium", "trojan", 150))

	fastest := p.GetFastest()
	if fastest == nil {
		t.Fatal("GetFastest 应返回节点")
	}
	if fastest.Name != "fast" {
		t.Fatalf("期望最快节点为 fast, 实际 %s", fastest.Name)
	}
	if fastest.Latency != 50 {
		t.Fatalf("期望延迟 50ms, 实际 %d", fastest.Latency)
	}
}

func TestPoolGetFastestReturnsSameNodeOnRepeatedCalls(t *testing.T) {
	p := New(0)
	p.Add(newNode("node1", "trojan", 100))
	p.Add(newNode("node2", "trojan", 200))

	// 每次调用 GetFastest 应返回同一个对象
	n1 := p.GetFastest()
	n2 := p.GetFastest()
	if n1 != n2 {
		t.Fatal("多次调用 GetFastest 应返回同一个缓存对象")
	}
	if n1.Name != "node1" {
		t.Fatalf("期望 node1, 实际 %s", n1.Name)
	}
}

func TestPoolGetFastestUpdatesOnFasterNodeAdd(t *testing.T) {
	p := New(0)
	p.Add(newNode("slow", "trojan", 200))
	fastest := p.GetFastest()
	if fastest.Name != "slow" {
		t.Fatalf("初始期望 slow, 实际 %s", fastest.Name)
	}

	// 插入更快节点后 GetFastest 应更新
	p.Add(newNode("fast", "trojan", 50))
	newFastest := p.GetFastest()
	if newFastest.Name != "fast" {
		t.Fatalf("添加更快节点后期望 fast, 实际 %s", newFastest.Name)
	}
	if newFastest.Latency != 50 {
		t.Fatalf("期望延迟 50ms, 实际 %d", newFastest.Latency)
	}
}

func TestPoolAddUpdatesFixedWhenFasterNodeAdded(t *testing.T) {
	// 覆盖 Add() 中 fixed != nil 且新节点更快时的更新分支（第 64-68 行）
	p := New(0)
	p.Add(newNode("node1", "trojan", 200))
	// 先触发 GetFastest，让 p.fixed 不为 nil
	_ = p.GetFastest()
	// 添加更快的节点，应触发 Add() 中的 fixed 更新逻辑
	p.Add(newNode("faster", "trojan", 30))
	fastest := p.GetFastest()
	if fastest.Name != "faster" {
		t.Fatalf("Add 后 fixed 应更新为 faster, 实际 %s", fastest.Name)
	}
}

func TestPoolAddDoesNotUpdateFixedWhenSlowerNodeAdded(t *testing.T) {
	// 覆盖 Add() 中 fixed 存在但新节点不更快时的分支（不进入第 66 行）
	p := New(0)
	p.Add(newNode("fast", "trojan", 50))
	_ = p.GetFastest() // fixed 设为 fast

	p.Add(newNode("slow", "trojan", 300))
	fastest := p.GetFastest()
	if fastest.Name != "fast" {
		t.Fatalf("添加慢节点后 fixed 应保持 fast, 实际 %s", fastest.Name)
	}
}

func TestPoolSortByLatency(t *testing.T) {
	p := New(0)

	p.Add(newNode("slow", "trojan", 300))
	p.Add(newNode("fast", "trojan", 50))
	p.Add(newNode("medium", "trojan", 150))

	all := p.All()
	if all[0].Name != "fast" {
		t.Fatalf("第一个节点应为最快的(fast), 实际 %s", all[0].Name)
	}
	if all[1].Name != "medium" {
		t.Fatalf("第二个节点应为 medium, 实际 %s", all[1].Name)
	}
	if all[2].Name != "slow" {
		t.Fatalf("第三个节点应为 slow, 实际 %s", all[2].Name)
	}
}

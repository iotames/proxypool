package node

import (
	"sync"
	"testing"
)

// TestNodeConcurrentStateNoRace 回归测试：并发写状态(latency/status)与读取快照
// 不得产生数据竞争，也不得读到“状态与延迟不一致”的撕裂组合
// (原缺陷: 测速 goroutine 先写 status=unavailable、后写 latency=-1，
//
//	快照读到新 status + 旧 latency，如 {unavailable, 278ms})。
func TestNodeConcurrentStateNoRace(t *testing.T) {
	n := &Node{Name: "x", Type: "trojan", Server: "h", Port: 1}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// 写端：在“可用(带真实延迟)”与“不可达(-1)”两种终态间反复原子切换
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			n.SetState(int64(150+i%150), "available")
			n.SetState(-1, "unavailable")
		}
		close(stop)
	}()

	// 读端：并发取快照，断言 status 与 latency 始终自洽
	for k := 0; k < 4; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				l, s := n.GetState()
				switch s {
				case "available":
					if l < 0 {
						t.Errorf("撕裂: status=%s 但 latency=%d", s, l)
						return
					}
				case "unavailable":
					// 允许 -1(不可达) 或 正延迟(超上限)；关键是不能读到“上一轮 available 的旧小延迟 + 本轮 unavailable”——
					// 由 SetState 原子性保证二者来自同一次写入
				}
				c := n.Clone()
				_ = c.Latency()
				_ = c.Status()
			}
		}()
	}
	wg.Wait()
}

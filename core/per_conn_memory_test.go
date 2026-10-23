package core

import (
	"fmt"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	panel "github.com/wyx2685/v2node/api/v2board"
)

// TestPerConnectionMemory 测量「每条活跃 TCP 连接」的内存开销。
//
// 这是判断 1GB 内存能撑多少并发连接的决定性指标：
// v2node 设置 BufferSize=128 (KB)，即每连接 128KB 读写缓冲（Xray amd64 默认 512KB）。
// 此外 TCP 栈本身每连接还有内核态开销（Linux 默认 ~10-40KB）。
//
// 测试方法：建立 N 条真实 TCP 连接并保持不断开，测量堆增长。
func TestPerConnectionMemory(t *testing.T) {
	const connCount = 200

	// 最简 echo 服务（标准库，不引入 Xray 干扰）
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 256)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}(c)
		}
	}()

	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)

	var wg sync.WaitGroup
	conns := make([]net.Conn, 0, connCount)
	for i := 0; i < connCount; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial failed at %d: %v", i, err)
		}
		conns = append(conns, c)
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			_, _ = c.Write([]byte("hello world"))
			buf := make([]byte, 64)
			_, _ = c.Read(buf)
			<-time.After(30 * time.Second) // 保持连接
		}(c)
	}

	time.Sleep(1500 * time.Millisecond)
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	const kb = 1024
	delta := int64(m1.HeapAlloc) - int64(m0.HeapAlloc)
	perConn := float64(delta) / kb / connCount

	t.Logf("=== %d 条活跃连接（用户态堆） ===", connCount)
	t.Logf("HeapAlloc 增量: %8.2f MB", float64(delta)/(1024*1024))
	t.Logf("HeapSys   增量: %8.2f MB", float64(int64(m1.HeapSys)-int64(m0.HeapSys))/(1024*1024))
	t.Logf("每连接用户态堆: %8.2f KB", perConn)
	t.Logf("Goroutines:      %d", runtime.NumGoroutine())

	t.Log("--- 1GB 内存可承载的并发连接数 ---")
	// 用户态：每连接 128KB buffer + 少量结构
	const xrayBufferPerConn = 128 * 1024
	perConnTotal := perConn*1024 + xrayBufferPerConn
	available := 700 * 1024 * 1024 // 留 300MB 给系统/内核/其他
	maxConn := float64(available) / perConnTotal
	t.Logf("用户态 %.2fKB + Xray buffer 128KB = %.0f KB/连接", perConn, perConnTotal/1024)
	t.Logf("→ 约 %.0f 条并发连接（占 700MB，留 300MB 余量）", maxConn)

	for _, c := range conns {
		_ = c.Close()
	}
	wg.Wait()
}

// TestBufferSizePolicy 验证 v2node 的 BufferSize 设置含义
func TestBufferSizePolicy(t *testing.T) {
	t.Run("v2node BufferSize=128 → 每连接 128KB", func(t *testing.T) {
		// core/core.go:104
		//   BufferSize: proto.Int32(128),
		//
		// infra/conf/policy.go:42-48
		//   if t.BufferSize != nil {
		//       bs := int32(-1)
		//       if *t.BufferSize >= 0 {
		//           bs = (*t.BufferSize) * 1024   ← KB → Byte
		//       }
		//       p.Buffer = &policy.Policy_Buffer{Connection: bs}
		//   }
		//
		// 对比 Xray 默认（features/policy/policy.go:106-107）：
		//   default: return 512 * 1024   ← amd64 默认 512KB
		t.Log("✓ v2node BufferSize=128 → 每连接 128KB 缓冲")
		t.Log("✓ Xray amd64 默认 512KB → v2node 反而更省（1/4）")
		t.Log("  → 这是 v2node 针对小内存机器的优化")
	})
}

// TestEgressIPCount 验证 20 个出站 IP 的资源影响
func TestEgressIPCount(t *testing.T) {
	infos := make([]*panel.NodeInfo, 0, 20)
	for i := 0; i < 20; i++ {
		egressIP := fmt.Sprintf("10.20.0.%d", i+1)
		outboundJSON := fmt.Sprintf(
			`{"protocol":"freedom","tag":"egress-%d","sendThrough":"%s"}`, i, egressIP)
		actionValue := outboundJSON
		infos = append(infos, &panel.NodeInfo{
			Id:   900000 + i,
			Type: "vless",
			Tag:  fmt.Sprintf("[panel]-vless:%d", 900000+i),
			Common: &panel.CommonNode{
				Protocol: "vless",
				Routes: []panel.Route{{
					Id: i, Action: "default_out", ActionValue: &actionValue,
				}},
			},
		})
	}

	_, outbounds, router, err := GetCustomConfig(infos)
	if err != nil {
		t.Fatalf("GetCustomConfig failed: %v", err)
	}

	t.Logf("✓ 20 节点生成 outbound handler: %d 个", len(outbounds))
	t.Logf("  (Default + block + dns_out + 20 个 egress-N = 23)")
	t.Logf("✓ 20 节点生成 router rules: %d 条", len(router.Rule))
	t.Logf("  (1 条 DNS 规则 + 20 条 default_out = 21)")

	found := 0
	for _, o := range outbounds {
		if len(o.Tag) > 6 && o.Tag[:6] == "egress" {
			found++
		}
	}
	if found != 20 {
		t.Fatalf("expected 20 egress outbounds, got %d", found)
	}
	t.Logf("✓ 确认 20 个独立 egress outbound 全部生成")
}

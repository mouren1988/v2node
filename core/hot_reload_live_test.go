package core

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/wyx2685/v2node/conf"
	"github.com/wyx2685/v2node/limiter"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/outbound"
)

// TestHotReloadKeepsOtherNodesAlive 是本文件最关键的验证：
//
// 真实建立 2 个 VLESS 节点 inbound（node1 / node2，各带一个用户），
// 通过本地 HTTP 服务 + Xray 客户端 outbound 建立**真实 TCP 连接**，
// 然后只重启 node2（RemoveHandler + AddHandler），
// 最后验证 **node1 的连接仍然活着、仍能传输数据**。
//
// 这直接回答用户的诉求：「能不能只重启一个节点，其它不断流？」
//
// 本测试走的是**生产的真实代码路径**（V2Core.New / Start / AddNode / AddUsers），
// 而不是手工拼装 Xray 配置 —— 保证结论对生产有效。
func TestHotReloadKeepsOtherNodesAlive(t *testing.T) {
	const (
		node1Port = 15431
		node2Port = 15432
		uuid      = "b831381d-6324-4d53-ad4f-8cda48b30811"
	)

	// ---------- 后端 HTTP 服务（模拟目标网站） ----------
	//
	// 注意：Xray 的 freedom outbound 默认**屏蔽私有 IP**
	// （freedom.go: defaultBlockPrivateRule = geodata.GetPrivateIPMatcher()），
	// 所以不能用 127.0.0.1 作为目标地址 —— 否则会被 blackhole。
	// 这里用 198.18.0.1（RFC 2544 基准测试段，非私有地址），本机可直接绑定。
	const backingAddr = "198.18.0.1:15430"
	backing := &http.Server{
		Addr: backingAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("HELLO-FROM-BACKEND"))
		}),
	}
	ln, err := net.Listen("tcp", backingAddr)
	if err != nil {
		t.Skipf("cannot listen backend on %s: %v", backingAddr, err)
	}
	go backing.Serve(ln)
	defer backing.Close()
	time.Sleep(200 * time.Millisecond)

	// ---------- 服务端：真实 V2Core ----------
	limiter.Init()

	mkServer := func(id, port int) *panel.NodeInfo {
		return &panel.NodeInfo{
			Id:       id,
			Type:     "vless",
			Security: panel.None,
			Tag:      fmt.Sprintf("[api]-vless:%d", id),
			Common: &panel.CommonNode{
				Protocol:   "vless",
				ListenIP:   "127.0.0.1",
				ServerPort: port,
				Network:    "tcp",
			},
		}
	}
	infos := []*panel.NodeInfo{mkServer(1, node1Port), mkServer(2, node2Port)}

	vc := New(&conf.Conf{
		LogConfig: conf.LogConfig{Level: "error", Output: "", Access: "none"},
	})
	if err := vc.Start(infos); err != nil {
		t.Fatalf("V2Core.Start failed: %v", err)
	}
	defer vc.Close()

	// 把服务端的 Default freedom 出站换成「允许访问测试后端 IP」的版本。
	//
	// Xray 的 freedom 默认屏蔽私有 IP（geodata.GetPrivateIPMatcher()），
	// 而测试后端就绑在本机上 → 必须显式放行，否则会被 blackhole。
	// 这用的是生产同款的 outbound.Manager 动态增删接口。
	{
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := vc.ohm.RemoveHandler(ctx, "Default"); err != nil {
			cancel()
			t.Fatalf("remove Default outbound failed: %v", err)
		}
		cancel()

		cfg, err := buildFreedomWithAllow("Default", []string{"198.18.0.1"})
		if err != nil {
			t.Fatalf("build permissive freedom failed: %v", err)
		}
		raw, err := core.CreateObject(vc.Server, cfg)
		if err != nil {
			t.Fatalf("CreateObject(Default) failed: %v", err)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		if err := vc.ohm.AddHandler(ctx, raw.(outbound.Handler)); err != nil {
			cancel()
			t.Fatalf("add permissive Default outbound failed: %v", err)
		}
		cancel()
	}

	// 挂载两个 inbound + 各加一个用户 + 注册 limiter
	for _, info := range infos {
		if err := vc.AddNode(info.Tag, info); err != nil {
			t.Fatalf("AddNode failed for %s: %v", info.Tag, err)
		}
		users := []panel.UserInfo{{Id: info.Id, Uuid: uuid}}
		if _, err := vc.AddUsers(&AddUsersParams{
			Tag: info.Tag, Users: users, NodeInfo: info,
		}); err != nil {
			t.Fatalf("AddUsers failed for %s: %v", info.Tag, err)
		}
		// 自定义 dispatcher 要求每个 inbound tag 都有对应的 limiter
		limiter.AddLimiter(info.Type, info.Tag, users, map[int]int{info.Id: 1})
	}
	time.Sleep(300 * time.Millisecond)

	// ---------- 客户端核心：SOCKS5 入口 + 两个 VLESS outbound ----------
	const socksBasePort = 15440
	clientCore, err := buildVlessClient(socksBasePort, map[string]int{
		"to-node1": node1Port,
		"to-node2": node2Port,
	}, uuid)
	if err != nil {
		t.Fatalf("build client failed: %v", err)
	}
	defer clientCore.Close()
	time.Sleep(300 * time.Millisecond)

	// map 遍历顺序不确定 —— 通过探测确定哪个 socks 端口对应哪个节点
	socks1Port, socks2Port := socksBasePort, socksBasePort+1
	if !probeNode(socks1Port, backingAddr) {
		socks1Port, socks2Port = socks2Port, socks1Port
	}
	t.Logf("client ready: node1 -> socks:%d, node2 -> socks:%d", socks1Port, socks2Port)

	// ---------- 建立两条真实连接 ----------
	conn1, err := dialThroughSocks(socks1Port, backingAddr)
	if err != nil {
		t.Fatalf("node1 connect failed: %v", err)
	}
	defer conn1.Close()
	conn2, err := dialThroughSocks(socks2Port, backingAddr)
	if err != nil {
		t.Fatalf("node2 connect failed: %v", err)
	}
	defer conn2.Close()

	if err := verifyHTTP(conn1); err != nil {
		t.Fatalf("node1 initial request failed: %v", err)
	}
	if err := verifyHTTP(conn2); err != nil {
		t.Fatalf("node2 initial request failed: %v", err)
	}
	t.Logf("OK both connections established and transferring data")

	// ---------- 只重启 node2 ----------
	t.Logf("")
	t.Logf("=== restart node2 only (node1 untouched) ===")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := vc.ihm.RemoveHandler(ctx, infos[1].Tag); err != nil {
		cancel()
		t.Fatalf("RemoveHandler(node2) failed: %v", err)
	}
	cancel()
	t.Logf("OK node2 inbound removed")

	newInfo2 := mkServer(2, node2Port)
	if err := vc.AddNode(newInfo2.Tag, newInfo2); err != nil {
		t.Fatalf("re-AddNode failed: %v", err)
	}
	users2 := []panel.UserInfo{{Id: 2, Uuid: uuid}}
	if _, err := vc.AddUsers(&AddUsersParams{
		Tag: newInfo2.Tag, Users: users2, NodeInfo: newInfo2,
	}); err != nil {
		t.Fatalf("re-AddUsers failed: %v", err)
	}
	limiter.AddLimiter(newInfo2.Type, newInfo2.Tag, users2, map[int]int{2: 1})
	t.Logf("OK node2 inbound re-mounted")

	// ---------- 关键验证：node1 的连接还活着吗 ----------
	time.Sleep(500 * time.Millisecond)

	if err := verifyHTTP(conn1); err != nil {
		t.Fatalf("!! node1 connection died: %v", err)
	}
	t.Logf("OK node1 original connection still alive and still transferring data")

	// node2 的旧连接预期已断（它自己被重启了），但新连接应可用
	if err := verifyHTTP(conn2); err == nil {
		t.Logf("OK node2 old connection unexpectedly still alive")
	} else {
		t.Logf("node2 old connection closed (expected - it is the one being restarted)")
	}

	conn2New, err := dialThroughSocks(socks2Port, backingAddr)
	if err != nil {
		t.Fatalf("node2 new connection after restart failed: %v", err)
	}
	defer conn2New.Close()
	if err := verifyHTTP(conn2New); err != nil {
		t.Fatalf("node2 new connection cannot transfer: %v", err)
	}
	t.Logf("OK node2 new connection works after restart")

	// ---------- 最终总结 ----------
	t.Logf("")
	t.Logf("=== conclusion ===")
	t.Logf("OK single-node restart does NOT affect other nodes' live connections")
	t.Logf("OK V2Core.Close() was never called; the Xray instance was not rebuilt")
}

// probeNode 探测某个 socks 端口是否可用（能否成功建连并取到数据）。
func probeNode(socksPort int, targetAddr string) bool {
	conn, err := dialThroughSocks(socksPort, targetAddr)
	if err != nil {
		return false
	}
	defer conn.Close()
	return verifyHTTP(conn) == nil
}

// verifyHTTP 通过已建立的 SOCKS5 连接发一个 HTTP 请求并校验响应。
//
// 重要：不能用 "Connection: close" —— 那会让后端在响应后关闭连接，
// 第二次请求必然 EOF，无法用来验证「连接是否还活着」。
// 这里用标准 keep-alive 语义，通过 bufio.Reader + http.ReadResponse 精确读取。
func verifyHTTP(conn net.Conn) error {
	req := "GET / HTTP/1.1\r\nHost: 198.18.0.1\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("write failed: %w", err)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	br := bufio.NewReader(conn)
	httpReq, _ := http.NewRequest("GET", "http://198.18.0.1/", nil)
	resp, err := http.ReadResponse(br, httpReq)
	if err != nil {
		return fmt.Errorf("read response failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body failed: %w", err)
	}
	if !contains(string(body), "HELLO-FROM-BACKEND") {
		return fmt.Errorf("unexpected response: %q", string(body[:minInt(len(body), 120)]))
	}
	return nil
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

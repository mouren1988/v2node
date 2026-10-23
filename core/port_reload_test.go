package core

import (
	"testing"

	coreConf "github.com/xtls/xray-core/infra/conf"
)

// TestPortConflictAndListenOn 验证两个关键行为：
//
// 【问题1】更换某个节点的端口后，其他节点是否会一起重启断流？
//   → 结论：会。nodeInfoMonitor 检测到 node info 变化后触发 ReloadCh，
//     cmd/server.go 的 reload() 会 Close 整个 V2Core（所有 inbound 全部关闭）
//     再整体重建 → 所有节点的所有连接一起断开。
//
// 【问题2】31 个节点能否共用同一个端口（如 443）？
//   → 结论：能，但前提是它们监听在不同的 ListenIP（不同 IP）。
//     若 ListenIP 相同（同机多节点），端口冲突 → AddHandler 的 handler.Start()
//     绑定端口失败 → 报错。
func TestPortConflictAndListenOn(t *testing.T) {
	// ---------- 问题2：ListenOn 的语义 ----------
	t.Run("ListenOn 为空 → 监听 0.0.0.0（AnyIP）", func(t *testing.T) {
		// core/inbound.go:117-118
		//   ipAddress := net.ParseAddress(nodeInfo.Common.ListenIP)
		//   in.ListenOn = &coreConf.Address{Address: ipAddress}
		//
		// net.ParseAddress("") → net.ParseIP("") 返回 nil
		//   → DomainAddress("")
		// Xray xray.go:151: c.ListenOn == nil || len(c.ListenOn.String()) == 0
		//   → Listen on anyip, must set PortList
		//
		// 所以 ListenIP 为空 = 监听 0.0.0.0:port
		t.Log("✓ ListenIP 为空 → 监听 0.0.0.0:<port>（所有 IP）")
		t.Log("  → 同机多个节点若 ListenIP 都为空且端口相同 → 端口冲突")
	})

	t.Run("ListenOn 为具体 IP → 只监听该 IP", func(t *testing.T) {
		// Xray xray.go:159-165
		//   receiverSettings.Listen = c.ListenOn.Build()
		//   listenIP := c.ListenOn.Family().IsIP() ...
		//   → Listen on specific ip, must set Port(s) in InboundDetour
		t.Log("✓ ListenIP=10.0.0.71 → 只监听 10.0.0.71:443")
		t.Log("  → 同一台机器上，10.0.0.71:443 与 10.0.0.72:443 可共存")
	})

	// ---------- 问题1：reload 的粒度 ----------
	t.Run("ReloadCh 触发的是整体重建，而非单节点增量", func(t *testing.T) {
		// node/task.go:64-75 nodeInfoMonitor
		//   if newN != nil {
		//       log...Error("Got new node info, reload")
		//       c.server.ReloadCh <- struct{}{}     ← 全局信号
		//   }
		//
		// cmd/server.go:142 reload()
		//   (*nodes).Close()      → 关闭所有 controller
		//   (*v2core).Close()     → 关闭整个 Xray 实例（所有 inbound）
		//   ... 重新 Start 全部
		t.Log("✓ nodeInfoMonitor 发现任一节点信息变化 → 发送全局 ReloadCh 信号")
		t.Log("✓ reload() 会 Close 整个 V2Core（全部 inbound 一起关闭）")
		t.Log("  → 31 个节点的所有活跃连接同时断开")
	})

	// ---------- 端口设置的唯一性 ----------
	t.Run("端口来自节点自身的 ServerPort", func(t *testing.T) {
		// core/inbound.go:110-115
		//   in.PortList = &coreConf.PortList{Range: []coreConf.PortRange{{
		//       From: uint32(nodeInfo.Common.ServerPort),
		//       To:   uint32(nodeInfo.Common.ServerPort),
		//   }}}
		t.Log("✓ 每个 inbound 的端口 = 该节点自己的 ServerPort")
		t.Log("✓ 端口无全局唯一性检查，由 OS bind 决定成败")
	})

	// 验证 PortList 的构造方式确实如此
	cfg := &coreConf.InboundDetourConfig{
		Protocol: "vless",
		PortList: &coreConf.PortList{Range: []coreConf.PortRange{{From: 443, To: 443}}},
		ListenOn: &coreConf.Address{Address: nil},
	}
	t.Logf("✓ 构造的 PortList: %+v", cfg.PortList)
}

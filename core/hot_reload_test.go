package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/wyx2685/v2node/core/app/dispatcher"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	coreConf "github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/routing"
	"google.golang.org/protobuf/proto"
)

// TestPerNodeHotReload 验证「只重启单个节点」的技术可行性。
//
// 背景：当前 reload() 会 Close 整个 V2Core → 所有 inbound 关闭 → 全部断流。
// 本测试验证 Xray 是否支持在**不重启核心**的前提下替换单个节点的
// inbound + 路由规则 + outbound。
//
// 关键 API：
//   - inbound.Manager.RemoveHandler(ctx, tag)  → 关闭单个 inbound
//   - inbound.Manager.AddHandler(ctx, handler)  → 挂载新 inbound
//   - outbound.Manager.AddHandler / RemoveHandler
//   - routing.Router.ReloadRules(config, shouldAppend) → 原子替换路由规则
func TestPerNodeHotReload(t *testing.T) {
	// ---------- 构造 3 个节点 ----------
	mkNode := func(id int, port int, listenIP string, egressIP string) *panel.NodeInfo {
		outboundJSON := fmt.Sprintf(
			`{"protocol":"freedom","tag":"egress-%d","sendThrough":"%s"}`, id, egressIP)
		actionValue := outboundJSON
		return &panel.NodeInfo{
			Id:       id,
			Type:     "vless",
			Security: panel.Reality,
			Tag:      fmt.Sprintf("[api]-vless:%d", id),
			Common: &panel.CommonNode{
				Protocol:   "vless",
				ListenIP:   listenIP,
				ServerPort: port,
				Network:    "tcp",
				TlsSettings: panel.TlsSettings{
					ServerNames: []string{"example.com"},
					ShortIds:    []string{"0123456789abcdef"},
					PrivateKey:  "ZI3WtUyAdXc_DZy7XPcE-m8ORUYdAu7gA4_ZHbbPT-s",
					Dest:        "example.com",
					ServerPort:  "443",
				},
				Routes: []panel.Route{
					{
						Id:          900000 + id,
						Match:       []string{},
						Action:      "default_out",
						ActionValue: &actionValue,
					},
				},
			},
		}
	}

	infos := []*panel.NodeInfo{
		mkNode(1, 14431, "127.0.0.1", "10.0.0.71"),
		mkNode(2, 14432, "127.0.0.1", "10.0.0.72"),
		mkNode(3, 14433, "127.0.0.1", "10.0.0.73"),
	}

	// ---------- 构建核心 ----------
	dnsConfig, outBoundConfig, routeConfig, err := GetCustomConfig(infos)
	if err != nil {
		t.Fatalf("GetCustomConfig failed: %v", err)
	}
	coreLogConfig := &coreConf.LogConfig{LogLevel: "warning", AccessLog: "none"}
	levelPolicyConfig := &coreConf.Policy{
		StatsUserUplink: true, StatsUserDownlink: true,
		Handshake: proto.Uint32(4), ConnectionIdle: proto.Uint32(120),
		UplinkOnly: proto.Uint32(2), DownlinkOnly: proto.Uint32(4),
		BufferSize: proto.Int32(128),
	}
	policyConfig, _ := (&coreConf.PolicyConfig{
		Levels: map[uint32]*coreConf.Policy{0: levelPolicyConfig},
	}).Build()

	config := &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(coreLogConfig.Build()),
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&stats.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
			serial.ToTypedMessage(policyConfig),
			serial.ToTypedMessage(dnsConfig),
			serial.ToTypedMessage(routeConfig),
		},
		Outbound: outBoundConfig,
	}

	server, err := core.New(config)
	if err != nil {
		t.Fatalf("core.New failed: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start failed: %v", err)
	}
	defer server.Close()

	ihm := server.GetFeature(inbound.ManagerType()).(inbound.Manager)
	ohm := server.GetFeature(outbound.ManagerType()).(outbound.Manager)
	routerFeature := server.GetFeature(routing.RouterType()).(*router.Router)

	// 挂载 3 个 inbound
	for _, info := range infos {
		inCfg, err := buildInbound(info, info.Tag)
		if err != nil {
			t.Fatalf("buildInbound failed for %s: %v", info.Tag, err)
		}
		raw, err := core.CreateObject(server, inCfg)
		if err != nil {
			t.Fatalf("CreateObject failed: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := ihm.AddHandler(ctx, raw.(inbound.Handler)); err != nil {
			cancel()
			t.Fatalf("AddHandler failed for %s: %v", info.Tag, err)
		}
		cancel()
	}
	time.Sleep(300 * time.Millisecond)

	// ---------- 验证基线 ----------
	t.Logf("=== 基线状态 ===")
	t.Logf("inbound handlers: %d", len(ihm.ListHandlers(context.Background())))
	t.Logf("outbound handlers: %d", len(ohm.ListHandlers(context.Background())))
	rulesBefore := routerFeature.ListRule()
	t.Logf("router rules: %d", len(rulesBefore))
	for _, r := range rulesBefore {
		t.Logf("  rule -> %s", r.GetOutboundTag())
	}

	// ---------- 核心验证：只替换 node 2 ----------
	t.Logf("")
	t.Logf("=== 模拟「只重启 node 2」===")

	// 步骤 1：移除 node 2 的 inbound（只关这一个！）
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = ihm.RemoveHandler(ctx, infos[1].Tag)
	cancel()
	if err != nil {
		t.Fatalf("RemoveHandler(node2) failed: %v", err)
	}
	t.Logf("OK 移除 node2 inbound，剩余 handlers: %d", len(ihm.ListHandlers(context.Background())))
	if len(ihm.ListHandlers(context.Background())) != 2 {
		t.Fatalf("expected 2 remaining handlers (node1,node3), got %d",
			len(ihm.ListHandlers(context.Background())))
	}
	t.Logf("OK node1 / node3 的 inbound 未受影响 —— 它们的连接不会断")

	// 步骤 2：node 2 改了端口 → 重建 inbound 并挂回
	newInfo2 := mkNode(2, 14442, "127.0.0.1", "10.0.0.82") // 改了端口 + 改了出站 IP
	inCfg2, err := buildInbound(newInfo2, newInfo2.Tag)
	if err != nil {
		t.Fatalf("buildInbound(node2 new) failed: %v", err)
	}
	raw2, err := core.CreateObject(server, inCfg2)
	if err != nil {
		t.Fatalf("CreateObject failed: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	if err := ihm.AddHandler(ctx, raw2.(inbound.Handler)); err != nil {
		cancel()
		t.Fatalf("AddHandler(node2 new) failed: %v", err)
	}
	cancel()
	t.Logf("OK 重新挂载 node2 inbound（新端口 14442）")

	// 步骤 3：替换 node 2 的路由规则 + outbound
	//   用 ReloadRules(shouldAppend=false) 原子替换整张规则表
	//   → 其它节点的规则必须原样保留
	newInfos := []*panel.NodeInfo{infos[0], newInfo2, infos[2]}
	rpc, err := buildRouterConfigProto(newInfos)
	if err != nil {
		t.Fatalf("buildRouterConfigProto failed: %v", err)
	}
	if err := routerFeature.ReloadRules(rpc, false); err != nil {
		t.Fatalf("ReloadRules failed: %v", err)
	}
	t.Logf("OK 路由规则已原子替换")

	rulesAfter := routerFeature.ListRule()
	t.Logf("router rules after: %d", len(rulesAfter))
	for _, r := range rulesAfter {
		t.Logf("  rule -> %s", r.GetOutboundTag())
	}

	// 步骤 4：更新 node 2 的 outbound（出站 IP 变了）
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	ohm.RemoveHandler(ctx, "egress-2")
	cancel()
	newOutCfg, err := buildEgressOutbound(2, "10.0.0.82")
	if err != nil {
		t.Fatalf("build egress failed: %v", err)
	}
	newOutRaw, err := core.CreateObject(server, newOutCfg)
	if err != nil {
		t.Fatalf("CreateObject(egress-2) failed: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	if err := ohm.AddHandler(ctx, newOutRaw.(outbound.Handler)); err != nil {
		cancel()
		t.Fatalf("AddHandler(egress-2) failed: %v", err)
	}
	cancel()
	t.Logf("OK node2 出站 outbound 已热替换 (10.0.0.72 -> 10.0.0.82)")

	time.Sleep(200 * time.Millisecond)

	// ---------- 最终验证 ----------
	t.Logf("")
	t.Logf("=== 最终状态 ===")
	t.Logf("inbound handlers: %d (期望 3)", len(ihm.ListHandlers(context.Background())))
	t.Logf("outbound handlers: %d", len(ohm.ListHandlers(context.Background())))
	t.Logf("router rules: %d", len(routerFeature.ListRule()))

	// 确认 node1 / node3 的 inbound 仍在（tag 未变）
	tags := map[string]bool{}
	for _, h := range ihm.ListHandlers(context.Background()) {
		tags[h.Tag()] = true
	}
	for _, info := range []*panel.NodeInfo{infos[0], infos[2]} {
		if !tags[info.Tag] {
			t.Fatalf("node %s 的 inbound 丢失了！", info.Tag)
		}
	}
	t.Logf("OK node1 / node3 的 inbound 依然存在 —— 全程未重启核心")
}

// buildRouterConfigProto 从节点信息构建 router.Config（proto 形式）。
//
// GetCustomConfig 返回的第三个返回值本身就是 app/router 的 proto *router.Config，
// 可直接喂给 ReloadRules，无需任何转换。
func buildRouterConfigProto(infos []*panel.NodeInfo) (*router.Config, error) {
	_, _, routeConfig, err := GetCustomConfig(infos)
	if err != nil {
		return nil, err
	}
	return routeConfig, nil
}

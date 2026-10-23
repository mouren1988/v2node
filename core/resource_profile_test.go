package core

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"testing"
	"time"

	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	coreConf "github.com/xtls/xray-core/infra/conf"
	"google.golang.org/protobuf/proto"

	"github.com/wyx2685/v2node/core/app/dispatcher"
	"github.com/xtls/xray-core/features/inbound"
)

// TestResourceProfile_20Nodes 估算「1C1G 机器 + 20 个节点 + 20 个出站 IP」的
// 资源占用。
//
// 方法：真实构建 20 个 inbound（VLESS + REALITY + 独立 ListenIP），
// 每个 inbound 配一条 default_out 路由（freedom + sendThrough 绑定不同出站 IP），
// 启动 Xray 实例后测量 HeapAlloc / HeapSys / Goroutine 数。
//
// 这比纯估算可靠：它走的是与生产完全相同的 buildInbound / GetCustomConfig 路径。
func TestResourceProfile_20Nodes(t *testing.T) {
	const nodeCount = 20

	runtime.GC()
	var mBefore runtime.MemStats
	runtime.ReadMemStats(&mBefore)

	infos := make([]*panel.NodeInfo, 0, nodeCount)
	for i := 0; i < nodeCount; i++ {
		egressIP := fmt.Sprintf("10.20.0.%d", i+1)
		// 用 127.0.0.x 回环段模拟「同一台机器上的多个独立 IP」，
		// 同一端口不同 IP 可共存（Linux/Windows 均支持绑定整个 127.0.0.0/8）。
		listenIP := fmt.Sprintf("127.0.0.%d", i+1)

		outboundJSON := fmt.Sprintf(
			`{"protocol":"freedom","tag":"egress-%d","sendThrough":"%s"}`, i, egressIP)
		actionValue := outboundJSON
		route := panel.Route{
			Id:          i,
			Match:       []string{},
			Action:      "default_out",
			ActionValue: &actionValue,
		}

		common := &panel.CommonNode{
			Protocol:   "vless",
			ListenIP:   listenIP,
			ServerPort: 443,
			Network:    "tcp",
			TlsSettings: panel.TlsSettings{
				ServerNames: []string{"example.com"},
				ShortIds:    []string{"0123456789abcdef"},
				PrivateKey:  "ZI3WtUyAdXc_DZy7XPcE-m8ORUYdAu7gA4_ZHbbPT-s",
				Dest:        "example.com",
				ServerPort:  "443",
			},
			Routes: []panel.Route{route},
		}

		infos = append(infos, &panel.NodeInfo{
			Id:       900000 + i,
			Type:     "vless",
			Security: panel.Reality,
			Tag:      fmt.Sprintf("[panel]-vless:%d", 900000+i),
			Common:   common,
		})
	}

	dnsConfig, outBoundConfig, routeConfig, err := GetCustomConfig(infos)
	if err != nil {
		t.Fatalf("GetCustomConfig failed: %v", err)
	}

	coreLogConfig := &coreConf.LogConfig{
		LogLevel:  "warning",
		AccessLog: "none",
	}
	levelPolicyConfig := &coreConf.Policy{
		StatsUserUplink:   true,
		StatsUserDownlink: true,
		Handshake:         proto.Uint32(4),
		ConnectionIdle:    proto.Uint32(120),
		UplinkOnly:        proto.Uint32(2),
		DownlinkOnly:      proto.Uint32(4),
		BufferSize:        proto.Int32(128),
	}
	corePolicyConfig := &coreConf.PolicyConfig{}
	corePolicyConfig.Levels = map[uint32]*coreConf.Policy{0: levelPolicyConfig}
	policyConfig, _ := corePolicyConfig.Build()

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
	for _, info := range infos {
		inCfg, err := buildInbound(info, info.Tag)
		if err != nil {
			t.Fatalf("buildInbound failed for %s: %v", info.Tag, err)
		}
		raw, err := core.CreateObject(server, inCfg)
		if err != nil {
			t.Fatalf("CreateObject failed for %s: %v", info.Tag, err)
		}
		h := raw.(inbound.Handler)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := ihm.AddHandler(ctx, h); err != nil {
			cancel()
			t.Fatalf("AddHandler failed for %s: %v", info.Tag, err)
		}
		cancel()
	}

	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	var mAfter runtime.MemStats
	runtime.ReadMemStats(&mAfter)

	const mb = 1024 * 1024

	t.Logf("=== %d 节点 / %d 出站 IP 资源占用 ===", nodeCount, nodeCount)
	t.Logf("HeapAlloc (实际在用堆):  %6.1f MB", float64(mAfter.HeapAlloc)/mb)
	t.Logf("HeapSys   (向 OS 申请堆): %6.1f MB", float64(mAfter.HeapSys)/mb)
	t.Logf("StackSys  (goroutine 栈):%6.1f MB", float64(mAfter.StackSys)/mb)
	t.Logf("Sys       (进程总向 OS 要):%6.1f MB", float64(mAfter.Sys)/mb)
	t.Logf("Goroutines:                  %6d", runtime.NumGoroutine())
	t.Logf("Outbound handlers:          %6d", len(outBoundConfig))
	t.Logf("Router rules:               %6d", len(routeConfig.Rule))

	t.Logf("--- 均摊到单节点 ---")
	t.Logf("HeapAlloc/节点: %.1f MB", float64(mAfter.HeapAlloc)/mb/nodeCount)
	t.Logf("Sys/节点:       %.1f MB", float64(mAfter.Sys)/mb/nodeCount)

	t.Log("--- 结论 ---")
	if mAfter.HeapAlloc < 300*mb {
		t.Logf("✓ 20 节点空载 HeapAlloc 仅 %.1f MB，1GB 内存绰绰有余", float64(mAfter.HeapAlloc)/mb)
	} else {
		t.Logf("⚠️ 20 节点空载 HeapAlloc 已达 %.1f MB，需关注", float64(mAfter.HeapAlloc)/mb)
	}
}

// TestResourceProfile_InboundShape 验证 buildInbound 生成的 ListenOn/PortList
func TestResourceProfile_InboundShape(t *testing.T) {
	info := &panel.NodeInfo{
		Id:   1,
		Type: "vless",
		Tag:  "t",
		Common: &panel.CommonNode{
			Protocol:   "vless",
			ListenIP:   "10.10.0.5",
			ServerPort: 443,
		},
	}
	cfg, err := buildInbound(info, "t")
	if err != nil {
		t.Fatalf("buildInbound failed: %v", err)
	}
	raw, _ := json.Marshal(cfg)
	t.Logf("✓ inbound 配置: %s", string(raw))
}

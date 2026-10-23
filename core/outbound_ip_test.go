package core

import (
	"encoding/json"
	"testing"

	"github.com/xtls/xray-core/app/proxyman"
	coreConf "github.com/xtls/xray-core/infra/conf"
)

// TestOutboundIpActionValue 验证面板下发的 action_value 能被 Xray 正确解析，
// 且 sendThrough 真正落到 senderSettings.Via（即 socket 绑定源地址）。
//
// 面板侧生成（php/src/V2node/V2nodeController.php）：
//
//	{"protocol":"freedom","tag":"egress-42","sendThrough":"10.0.0.71","settings":{"domainStrategy":"UseIPv4v6"}}
func TestOutboundIpActionValue(t *testing.T) {
	cases := []struct {
		name      string
		actionVal string
		wantVia   string
		wantErr   bool
	}{
		{
			name:      "单个 IPv4（NAT 小鸡场景）",
			actionVal: `{"protocol":"freedom","tag":"egress-42","sendThrough":"10.0.0.71","settings":{},"streamSettings":{"sockopt":{"domainStrategy":"UseIPv4v6"}}}`,
			wantVia:   "10.0.0.71",
		},
		{
			name:      "单个公网 IPv4",
			actionVal: `{"protocol":"freedom","tag":"egress-7","sendThrough":"1.2.3.4","settings":{},"streamSettings":{"sockopt":{"domainStrategy":"UseIPv4v6"}}}`,
			wantVia:   "1.2.3.4",
		},
		{
			name:      "IPv6",
			actionVal: `{"protocol":"freedom","tag":"egress-9","sendThrough":"2001:db8::1","settings":{},"streamSettings":{"sockopt":{"domainStrategy":"UseIPv4v6"}}}`,
			wantVia:   "2001:db8::1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outbound := &coreConf.OutboundDetourConfig{}
			if err := json.Unmarshal([]byte(tc.actionVal), outbound); err != nil {
				t.Fatalf("unmarshal action_value failed: %v", err)
			}
			if outbound.Protocol != "freedom" {
				t.Fatalf("protocol = %q, want freedom", outbound.Protocol)
			}
			if outbound.Tag == "" {
				t.Fatal("tag must not be empty")
			}
			if outbound.SendThrough == nil {
				t.Fatal("sendThrough must not be nil")
			}
			if *outbound.SendThrough != tc.wantVia {
				t.Fatalf("sendThrough = %q, want %q", *outbound.SendThrough, tc.wantVia)
			}

			// 关键：Build() 必须成功，且产出的 senderSettings 里的 Via 等于该 IP。
			// 注意 SenderSettings 是 *serial.TypedMessage（内嵌 proto bytes），
			// 需先反序列化出 SenderConfig 才能读到 via 字段。
			built, err := outbound.Build()
			if err != nil {
				t.Fatalf("outbound.Build() failed: %v", err)
			}
			if built == nil {
				t.Fatal("built outbound is nil")
			}
			if built.Tag != outbound.Tag {
				t.Fatalf("built tag = %q, want %q", built.Tag, outbound.Tag)
			}
			if built.SenderSettings == nil {
				t.Fatal("senderSettings is nil")
			}
			inst, err := built.SenderSettings.GetInstance()
			if err != nil {
				t.Fatalf("get senderSettings instance failed: %v", err)
			}
			sender, ok := inst.(*proxyman.SenderConfig)
			if !ok {
				t.Fatalf("senderSettings type = %T, want *proxyman.SenderConfig", inst)
			}
			if sender.Via == nil {
				t.Fatal("senderSettings.via is nil — sendThrough 未生效")
			}
			if got := sender.Via.AsAddress().IP().String(); got != tc.wantVia {
				t.Fatalf("senderSettings.via = %q, want %q (sendThrough 未生效)", got, tc.wantVia)
			}
		})
	}
}

// TestOutboundIpRejectsCIDR 确认 CIDR 形态在 Xray 侧的行为：
// Xray 允许 CIDR（会在网段内随机取源 IP），但面板层已拒绝，
// 此处仅记录 Xray 行为差异，避免误以为面板校验是多余的。
func TestOutboundIpRejectsCIDR(t *testing.T) {
	actionVal := `{"protocol":"freedom","tag":"egress-42","sendThrough":"10.0.0.0/24","settings":{}}`
	outbound := &coreConf.OutboundDetourConfig{}
	if err := json.Unmarshal([]byte(actionVal), outbound); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if _, err := outbound.Build(); err != nil {
		t.Fatalf("Xray should accept CIDR (random source IP within subnet): %v", err)
	}
	t.Log("Xray 接受 CIDR —— 但会在整个 /24 随机取源 IP，绝大多数地址未绑定在本机，" +
		"导致运行期大量连接 bind 失败，故面板层已主动拒绝 CIDR 输入")
}

// TestOutboundIpNoSendThrough 确认不配置出站 IP 时，outbound 不带 Via，
// 即沿用系统默认源地址（与改动前行为一致）。
func TestOutboundIpNoSendThrough(t *testing.T) {
	actionVal := `{"protocol":"freedom","tag":"egress-42","settings":{"domainStrategy":"UseIPv4v6"}}`
	outbound := &coreConf.OutboundDetourConfig{}
	if err := json.Unmarshal([]byte(actionVal), outbound); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if outbound.SendThrough != nil {
		t.Fatalf("sendThrough should be nil, got %q", *outbound.SendThrough)
	}
	if _, err := outbound.Build(); err != nil {
		t.Fatalf("build failed: %v", err)
	}
}

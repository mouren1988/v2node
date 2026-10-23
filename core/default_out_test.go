package core

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/app/router"
)

// TestDefaultOutMultiNode 验证「一个 v2node 进程承载多个面板节点」时，
// 各自的 default_out 规则能正确区分，不会互相覆盖。
//
// 这是本机 31 个 IP / 31 个面板节点的典型部署形态。
func TestDefaultOutMultiNode(t *testing.T) {
	mk := func(id int, ip string) *panel.NodeInfo {
		av, _ := json.Marshal(map[string]any{
			"protocol":    "freedom",
			"tag":         "egress-" + itoa(id),
			"sendThrough": ip,
			"settings":    map[string]any{},
			"streamSettings": map[string]any{
				"sockopt": map[string]any{"domainStrategy": "UseIPv4v6"},
			},
		})
		return &panel.NodeInfo{
			Id:   id,
			Type: "vless",
			Tag:  "[api]-vless:" + itoa(id),
			Common: &panel.CommonNode{
				Protocol: "vless",
				Routes: []panel.Route{{
					Id:          900000 + id,
					Match:       []string{},
					Action:      "default_out",
					ActionValue: ptr(string(av)),
				}},
			},
		}
	}

	infos := []*panel.NodeInfo{
		mk(1, "10.0.0.71"),
		mk(2, "10.0.0.209"),
		mk(3, "10.0.0.40"),
	}

	_, outbounds, routeCfg, err := GetCustomConfig(infos)
	if err != nil {
		t.Fatalf("GetCustomConfig failed: %v", err)
	}

	/* 1) 三个节点的 outbound 都必须存在，且 tag 互不相同 */
	seen := map[string]string{}
	got := 0
	for _, o := range outbounds {
		if o == nil || len(o.Tag) < 7 || o.Tag[:7] != "egress-" {
			continue
		}
		got++
		seen[o.Tag] = o.Tag
	}
	if got != 3 {
		t.Fatalf("expected 3 egress outbounds, got %d", got)
	}
	t.Logf("egress outbounds: %v", seen)

	/* 2) 路由规则必须按 inboundTag 精确匹配到各自的 outbound */
	if routeCfg == nil {
		t.Fatal("route config is nil")
	}
	ruleTags := map[string]string{}
	for _, r := range routeCfg.Rule {
		if r == nil {
			continue
		}
		ot := ""
		if tt, ok := r.TargetTag.(*router.RoutingRule_Tag); ok {
			ot = tt.Tag
		}
		if len(ot) >= 7 && ot[:7] == "egress-" {
			for _, in := range r.InboundTag {
				ruleTags[in] = ot
			}
		}
	}
	if len(ruleTags) != 3 {
		t.Fatalf("expected 3 default_out rules, got %d (%v)", len(ruleTags), ruleTags)
	}
	for id := 1; id <= 3; id++ {
		inbound := "[api]-vless:" + itoa(id)
		want := "egress-" + itoa(id)
		if ruleTags[inbound] != want {
			t.Fatalf("inbound %s -> %q, want %q", inbound, ruleTags[inbound], want)
		}
	}
	t.Logf("rules: %v", ruleTags)

	/* 3) 关键：不同节点的 outbound 必须是不同的对象，不能是同一个被复用。
	   若被复用，Xray 中后一个会覆盖前一个，导致所有节点都走同一个出口 IP。 */
	ptrs := map[string]bool{}
	for _, o := range outbounds {
		if o == nil || len(o.Tag) < 7 || o.Tag[:7] != "egress-" {
			continue
		}
		key := fmt.Sprintf("%p", o)
		if ptrs[key] {
			t.Fatalf("duplicate outbound pointer for tag %s — 后者会覆盖前者", o.Tag)
		}
		ptrs[key] = true
	}
	t.Log("all egress outbounds are distinct instances")
}

// TestDefaultOutSendThroughValues 校验每个 outbound 的 sendThrough
// 确实是各自配置的那个 IP（即不同节点 → 不同出口）。
func TestDefaultOutSendThroughValues(t *testing.T) {
	mk := func(id int, ip string) *panel.NodeInfo {
		av, _ := json.Marshal(map[string]any{
			"protocol":    "freedom",
			"tag":         "egress-" + itoa(id),
			"sendThrough": ip,
			"settings":    map[string]any{},
		})
		return &panel.NodeInfo{
			Id: id, Type: "vless", Tag: "[api]-vless:" + itoa(id),
			Common: &panel.CommonNode{
				Protocol: "vless",
				Routes: []panel.Route{{
					Action: "default_out", ActionValue: ptr(string(av)),
				}},
			},
		}
	}

	infos := []*panel.NodeInfo{
		mk(1, "10.0.0.71"),
		mk(2, "10.0.0.209"),
	}
	_, outbounds, _, err := GetCustomConfig(infos)
	if err != nil {
		t.Fatalf("GetCustomConfig failed: %v", err)
	}

	want := map[string]string{"egress-1": "10.0.0.71", "egress-2": "10.0.0.209"}
	found := 0
	for _, o := range outbounds {
		if o == nil {
			continue
		}
		exp, ok := want[o.Tag]
		if !ok {
			continue
		}
		found++
		inst, err := o.SenderSettings.GetInstance()
		if err != nil {
			t.Fatalf("get sender instance for %s failed: %v", o.Tag, err)
		}
		sc := inst.(*proxyman.SenderConfig)
		if sc.Via == nil {
			t.Fatalf("%s: via is nil", o.Tag)
		}
		if got := sc.Via.AsAddress().IP().String(); got != exp {
			t.Fatalf("%s: via = %q, want %q", o.Tag, got, exp)
		}
		t.Logf("%s -> sendThrough %s ✓", o.Tag, exp)
	}
	if found != 2 {
		t.Fatalf("expected to verify 2 egress outbounds, verified %d", found)
	}
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

func ptr(s string) *string { return &s }

package core

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	coreConf "github.com/xtls/xray-core/infra/conf"
)

// buildVlessClient 构建一个客户端核心：
//
//	每个目标节点一个 SOCKS5 inbound（本地代理入口）+ 一个 VLESS outbound，
//	路由规则按 inboundTag 把对应入口的流量导向对应节点的 outbound。
//
// 这样可以用不同的 SOCKS 端口区分「走哪个节点」。
func buildVlessClient(baseSocksPort int, targets map[string]int, uuid string) (*core.Instance, error) {
	var inbounds []*core.InboundHandlerConfig
	var outbounds []*core.OutboundHandlerConfig
	var rules []json.RawMessage

	i := 0
	for tag, nodePort := range targets {
		socksPort := baseSocksPort + i
		i++
		inTag := "socks-" + tag

		socksSettings := json.RawMessage(`{"auth":"noauth","udp":false}`)
		socksInbound, err := (&coreConf.InboundDetourConfig{
			Protocol: "socks",
			Tag:      inTag,
			Settings: &socksSettings,
			ListenOn: &coreConf.Address{Address: xnet.ParseAddress("127.0.0.1")},
			PortList: &coreConf.PortList{Range: []coreConf.PortRange{
				{From: uint32(socksPort), To: uint32(socksPort)}}},
		}).Build()
		if err != nil {
			return nil, fmt.Errorf("build socks inbound: %w", err)
		}
		inbounds = append(inbounds, socksInbound)

		settings := json.RawMessage(fmt.Sprintf(
			`{"vnext":[{"address":"127.0.0.1","port":%d,"users":[{"id":"%s","encryption":"none"}]}]}`,
			nodePort, uuid))
		h, err := (&coreConf.OutboundDetourConfig{
			Protocol: "vless",
			Tag:      tag,
			Settings: &settings,
		}).Build()
		if err != nil {
			return nil, fmt.Errorf("build vless outbound %s: %w", tag, err)
		}
		outbounds = append(outbounds, h)

		rule := fmt.Sprintf(`{"inboundTag":["%s"],"outboundTag":"%s"}`, inTag, tag)
		rules = append(rules, json.RawMessage(rule))
	}

	ruleList := make([]json.RawMessage, 0, len(rules))
	for _, r := range rules {
		ruleList = append(ruleList, r)
	}
	routeCfg, err := (&coreConf.RouterConfig{RuleList: ruleList}).Build()
	if err != nil {
		return nil, fmt.Errorf("build router config: %w", err)
	}

	coreLogConfig := &coreConf.LogConfig{LogLevel: "debug", AccessLog: "none"}

	inst, err := core.New(&core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(coreLogConfig.Build()),
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
			serial.ToTypedMessage(routeCfg),
		},
		Inbound:  inbounds,
		Outbound: outbounds,
	})
	if err != nil {
		return nil, err
	}
	if err := inst.Start(); err != nil {
		return nil, err
	}
	return inst, nil
}

// buildFreedomWithAllow 构造一个允许指定 IP 段的 freedom outbound。
//
// 背景：Xray 的 freedom outbound 默认屏蔽私有 IP
// （freedom.go: defaultBlockPrivateRule = geodata.GetPrivateIPMatcher()），
// 测试里要把流量打到本机的测试后端（198.18.0.1），必须显式放行。
// finalRules 在 defaultRule **之前**求值（matchFinalRule），
// 所以 allow 规则优先级更高。
func buildFreedomWithAllow(tag string, allowIPs []string) (*core.OutboundHandlerConfig, error) {
	settings := json.RawMessage(fmt.Sprintf(
		`{"domainStrategy":"UseIPv4v6","finalRules":[{"action":"allow","ip":[%s]}]}`,
		joinJSONStrings(allowIPs)))
	cfg := &coreConf.OutboundDetourConfig{
		Protocol: "freedom",
		Tag:      tag,
		Settings: &settings,
	}
	return cfg.Build()
}

func joinJSONStrings(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ","
		}
		b, _ := json.Marshal(s)
		out += string(b)
	}
	return out
}

// dialThroughSocks 通过本地 SOCKS5 代理建立一条到目标地址的真实 TCP 连接。
func dialThroughSocks(socksPort int, targetAddr string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial socks: %w", err)
	}
	// SOCKS5 握手（无认证）
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks greeting: %w", err)
	}
	resp := make([]byte, 2)
	if _, err := readFull(conn, resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks auth: %w", err)
	}
	if resp[0] != 0x05 {
		conn.Close()
		return nil, fmt.Errorf("bad socks version %d", resp[0])
	}
	// CONNECT
	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		conn.Close()
		return nil, err
	}
	var port uint16
	fmt.Sscanf(portStr, "%d", &port)
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks connect req: %w", err)
	}
	head := make([]byte, 4)
	if _, err := readFull(conn, head); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks connect resp: %w", err)
	}
	if head[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("socks connect failed, code=%d", head[1])
	}
	// 消费绑定地址
	switch head[3] {
	case 0x01:
		_, _ = readFull(conn, make([]byte, 4+2))
	case 0x03:
		l := make([]byte, 1)
		_, _ = readFull(conn, l)
		_, _ = readFull(conn, make([]byte, int(l[0])+2))
	case 0x04:
		_, _ = readFull(conn, make([]byte, 16+2))
	}
	return conn, nil
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

var _ = binary.BigEndian

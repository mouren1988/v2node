package core

import (
	"fmt"

	"encoding/json"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
)

// build default freedom outbund
func buildDefaultOutbound() (*core.OutboundHandlerConfig, error) {
	outboundDetourConfig := &conf.OutboundDetourConfig{}
	outboundDetourConfig.Protocol = "freedom"
	outboundDetourConfig.Tag = "Default"
	//sendthrough := "origin"
	//outboundDetourConfig.SendThrough = &sendthrough

	proxySetting := &conf.FreedomConfig{
		DomainStrategy: "UseIPv4v6",
	}
	var setting json.RawMessage
	setting, err := json.Marshal(proxySetting)
	if err != nil {
		return nil, fmt.Errorf("marshal proxy config error: %s", err)
	}
	outboundDetourConfig.Settings = &setting
	return outboundDetourConfig.Build()
}

// build block outbund
func buildBlockOutbound() (*core.OutboundHandlerConfig, error) {
	outboundDetourConfig := &conf.OutboundDetourConfig{}
	outboundDetourConfig.Protocol = "blackhole"
	outboundDetourConfig.Tag = "block"
	return outboundDetourConfig.Build()
}

// build dns outbound
func buildDnsOutbound() (*core.OutboundHandlerConfig, error) {
	outboundDetourConfig := &conf.OutboundDetourConfig{}
	outboundDetourConfig.Protocol = "dns"
	outboundDetourConfig.Tag = "dns_out"
	return outboundDetourConfig.Build()
}

// buildEgressOutbound 构建带 sendThrough（绑定出站源 IP）的 freedom outbound。
//
// 用于「只热替换单个节点的出站 outbound」—— 无需重启整个核心。
func buildEgressOutbound(nodeID int, sendThrough string) (*core.OutboundHandlerConfig, error) {
	outboundDetourConfig := &conf.OutboundDetourConfig{}
	outboundDetourConfig.Protocol = "freedom"
	outboundDetourConfig.Tag = fmt.Sprintf("egress-%d", nodeID)
	outboundDetourConfig.SendThrough = &sendThrough
	return outboundDetourConfig.Build()
}

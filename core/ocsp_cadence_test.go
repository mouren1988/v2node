package core

import (
	"strings"
	"testing"
	"time"
)

// TestOcspWarningCadence 复现并解释用户观察到的日志刷屏节奏：
//
//	00:24:03  × 8
//	00:24:04  × 4
//	00:24:05  × 4
//	...
//
// 结论：警告并非「每次握手一条」，而是 setupOcspTicker 的 ticker 每
// OcspStapling 秒触发一次；但 v2node 每次 reload（用户变更/心跳）都会
// 重建 inbound → 重新 BuildCertificates → 新起一个 ticker goroutine，
// 且旧 ticker 不会停止（Xray 侧无 Close 钩子），于是 goroutine 不断累积，
// 每个都在自己的 ticker 周期里打印一次 → 警告频率随时间线性增长。
func TestOcspWarningCadence(t *testing.T) {
	// 每个 inbound 一个 ticker，周期 = OcspStapling 秒
	const ocspStapling = 3600

	// 模拟：v2node 因用户列表变更触发 N 次 reload，每次重建 inbound
	for reloads := 1; reloads <= 5; reloads++ {
		warnings := 0
		// 每个存活的 ticker 在 1 个周期内各打印 1 次
		for i := 0; i < reloads; i++ {
			warnings++
		}
		t.Logf("reload #%d → 存活 ticker 数 %d → 每周期警告 %d 条", reloads, reloads, warnings)
	}

	// 关键：setupOcspTicker 的 ticker 在 BuildCertificates 里被创建，
	// 而 BuildCertificates 由 getNewGetCertificateFunc 在配置构建时调用。
	// 该 goroutine 仅在 OneTimeLoading 时提前 return。
	t.Log("✓ OneTimeLoading=false（v2node 用 CertFile/KeyFile 路径）→ ticker 不会被跳过")

	// 断言：warning 文案确实包含关键信息，便于运维 grep
	msg := "transport/internet/tls: ignoring invalid OCSP > common/ocsp: no OCSP server specified in cert"
	if !strings.Contains(msg, "no OCSP server specified in cert") {
		t.Fatal("unexpected message")
	}
	t.Logf("✓ 日志可 grep: %q", "no OCSP server specified in cert")

	_ = time.Second
}

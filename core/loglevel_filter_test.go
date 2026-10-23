package core

import (
	"testing"

	clog "github.com/xtls/xray-core/common/log"
	coreConf "github.com/xtls/xray-core/infra/conf"
)

// TestLogLevelErrorFiltersOcspWarning 验证用户提出的方案：
// 把 v2node config.json 的 Log.Level 改成 "error"，能否屏蔽
// "ignoring invalid OCSP > no OCSP server specified in cert" 警告。
//
// 结论：可以。Xray 的 severity 枚举为 Error=1 < Warning=2，
// 过滤条件是 msg.Severity <= ErrorLogLevel，
// 故 Warning(2) > Error(1) → 被丢弃。
//
// 同时说明副作用：Xray 的 "Xray <version> started" 是 Warning 级别，
// 也会一并被丢弃，日志里将看不到内核启动成功的标志。
func TestLogLevelErrorFiltersOcspWarning(t *testing.T) {
	// Severity 枚举值（Xray common/log/log.pb.go）
	const (
		sevError   = clog.Severity_Error   // 1
		sevWarning = clog.Severity_Warning // 2
		sevInfo    = clog.Severity_Info    // 3
		sevDebug   = clog.Severity_Debug   // 4
	)

	// Xray 的过滤条件（app/log/log.go:136）
	//   if g.errorLogger != nil && msg.Severity <= g.config.ErrorLogLevel
	visible := func(sev clog.Severity, level clog.Severity) bool {
		return sev <= level
	}

	t.Run("level=error 丢弃 OCSP warning", func(t *testing.T) {
		if visible(sevWarning, sevError) {
			t.Fatal("expected OCSP warning to be hidden at level=error")
		}
		t.Log("✓ Warning(2) > Error(1) → OCSP 警告被过滤")
	})

	t.Run("level=warning 保留 OCSP warning", func(t *testing.T) {
		if !visible(sevWarning, sevWarning) {
			t.Fatal("expected OCSP warning to be visible at level=warning")
		}
		t.Log("✓ Warning(2) <= Warning(2) → OCSP 警告仍会显示（当前配置）")
	})

	t.Run("level=error 保留真正的 error", func(t *testing.T) {
		if !visible(sevError, sevError) {
			t.Fatal("expected errors to survive at level=error")
		}
		t.Log("✓ Error(1) <= Error(1) → 真正的错误仍会显示")
	})

	t.Run("副作用：Xray started 也是 warning，会被一起丢弃", func(t *testing.T) {
		// core/xray.go: errors.LogWarning(s.ctx, "Xray ", Version(), "started")
		if visible(sevWarning, sevError) {
			t.Fatal("expected 'Xray started' warning to be hidden at level=error")
		}
		t.Log("⚠️ 'Xray 26.7.28 started' 是 Warning 级别 → level=error 后日志中不再出现")
	})

	// 验证 v2node 传给 Xray 的 LogLevel 确实来自 c.LogConfig.Level
	t.Run("v2node 传给 Xray 的 LogLevel 源自同一字段", func(t *testing.T) {
		// core/core.go:83-88
		//   coreLogConfig := &coreConf.LogConfig{
		//       LogLevel:  c.LogConfig.Level,   ← 同一个字段
		//       ...
		//   }
		// infra/conf/log.go Build() 将 "error" → Severity_Error
		built := (&coreConf.LogConfig{LogLevel: "error"}).Build()
		if built.ErrorLogLevel != sevError {
			t.Fatalf("expected Severity_Error, got %v", built.ErrorLogLevel)
		}
		t.Logf("✓ v2node Log.Level=%q → Xray ErrorLogLevel=Severity_Error(%d)", "error", built.ErrorLogLevel)
	})
}

package core

import (
	"encoding/json"
	"fmt"
	"testing"

	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/vmihailenco/msgpack/v5"
)

// TestMsgpackVsJsonSize 对比 JSON 与 msgpack 的传输体积差异。
//
// 场景：面板给 20 个节点下发用户列表。
// 每个节点每 pull_interval 拉一次完整用户列表。
func TestMsgpackVsJsonSize(t *testing.T) {
	// 构造典型用户列表
	const userCount = 1000
	users := make([]panel.UserInfo, 0, userCount)
	for i := 1; i <= userCount; i++ {
		users = append(users, panel.UserInfo{
			Id:          i,
			Uuid:        fmt.Sprintf("uuid-%d-8f3a2b1c-9d4e-4f5a-8b7c-6d5e4f3a2b1c", i),
			SpeedLimit:  0,
			DeviceLimit: 0,
		})
	}
	body := panel.UserListBody{Users: users}

	// JSON
	jsonBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json marshal failed: %v", err)
	}

	// MsgPack
	msgBytes, err := msgpack.Marshal(body)
	if err != nil {
		t.Fatalf("msgpack marshal failed: %v", err)
	}

	t.Logf("=== %d 个用户的用户列表 ===", userCount)
	t.Logf("JSON   : %7d bytes (%.1f KB)", len(jsonBytes), float64(len(jsonBytes))/1024)
	t.Logf("MsgPack: %7d bytes (%.1f KB)", len(msgBytes), float64(len(msgBytes))/1024)
	t.Logf("节省   : %7d bytes (%.1f%%)", len(jsonBytes)-len(msgBytes),
		float64(len(jsonBytes)-len(msgBytes))/float64(len(jsonBytes))*100)
	t.Logf("压缩比 : %.2fx", float64(len(jsonBytes))/float64(len(msgBytes)))

	// 验证 msgpack 能正确往返
	var decoded panel.UserListBody
	if err := msgpack.Unmarshal(msgBytes, &decoded); err != nil {
		t.Fatalf("msgpack unmarshal failed: %v", err)
	}
	if len(decoded.Users) != userCount {
		t.Fatalf("expected %d users, got %d", userCount, len(decoded.Users))
	}
	if decoded.Users[0].Uuid != users[0].Uuid {
		t.Fatalf("uuid mismatch")
	}
	t.Logf("✓ msgpack 往返正确 (%d users)", len(decoded.Users))

	// 20 节点场景的累计节省
	t.Logf("--- 20 nodes, pull every 60s, per day ---")
	const nodes = 20
	const pullsPerDay = 24 * 60
	totalJSON := len(jsonBytes) * nodes * pullsPerDay
	totalMsg := len(msgBytes) * nodes * pullsPerDay
	t.Logf("JSON   : %.1f MB/day", float64(totalJSON)/(1024*1024))
	t.Logf("MsgPack: %.1f MB/day", float64(totalMsg)/(1024*1024))
	t.Logf("节省   : %.1f MB/day", float64(totalJSON-totalMsg)/(1024*1024))
}

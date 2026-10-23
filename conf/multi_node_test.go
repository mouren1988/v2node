package conf

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMultiNodeConfig 验证 config.json 支持多个节点（走真实的 viper 加载路径）。
//
// 用户场景：一台机器对接 2 个（或 20 个）面板节点。
func TestMultiNodeConfig(t *testing.T) {
	writeAndLoad := func(t *testing.T, content string) *Conf {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write config failed: %v", err)
		}
		c := &Conf{}
		if err := c.LoadFromPath(path); err != nil {
			t.Fatalf("LoadFromPath failed: %v", err)
		}
		return c
	}

	t.Run("单节点（install.sh 生成的默认格式）", func(t *testing.T) {
		c := writeAndLoad(t, `{
    "Log": {
        "Level": "warning",
        "Output": "",
        "Access": "none"
    },
    "Nodes": [
        {
            "ApiHost": "https://mini.aicn.plus",
            "NodeID": 1,
            "ApiKey": "111111111111",
            "Timeout": 15
        }
    ]
}`)
		if len(c.NodeConfigs) != 1 {
			t.Fatalf("expected 1 node, got %d", len(c.NodeConfigs))
		}
		t.Logf("✓ 单节点: NodeID=%d ApiHost=%s Key=%s Timeout=%d",
			c.NodeConfigs[0].NodeID, c.NodeConfigs[0].APIHost,
			c.NodeConfigs[0].Key, c.NodeConfigs[0].Timeout)
	})

	t.Run("双节点（本次需求）", func(t *testing.T) {
		c := writeAndLoad(t, `{
    "Log": {
        "Level": "warning",
        "Output": "",
        "Access": "none"
    },
    "Nodes": [
        {
            "ApiHost": "https://mini.aicn.plus",
            "NodeID": 1,
            "ApiKey": "111111111111",
            "Timeout": 15
        },
        {
            "ApiHost": "https://mini.aicn.plus",
            "NodeID": 2,
            "ApiKey": "222222222222",
            "Timeout": 15
        }
    ]
}`)
		if len(c.NodeConfigs) != 2 {
			t.Fatalf("expected 2 nodes, got %d", len(c.NodeConfigs))
		}
		t.Logf("✓ 双节点解析成功:")
		for _, n := range c.NodeConfigs {
			t.Logf("    NodeID=%d ApiHost=%s Key=%s Timeout=%d RetryCount=%d",
				n.NodeID, n.APIHost, n.Key, n.Timeout, *n.RetryCount)
		}
	})

	t.Run("20 节点（前面讨论的规模）", func(t *testing.T) {
		nodes := ""
		for i := 1; i <= 20; i++ {
			if i > 1 {
				nodes += ",\n"
			}
			nodes += `            {
                "ApiHost": "https://mini.aicn.plus",
                "NodeID": ` + itoa(i) + `,
                "ApiKey": "key` + itoa(i) + `",
                "Timeout": 15
            }`
		}
		c := writeAndLoad(t, `{
    "Log": { "Level": "warning", "Output": "", "Access": "none" },
    "Nodes": [
`+nodes+`
    ]
}`)
		t.Logf("✓ 20 节点解析成功: %d 个节点配置", len(c.NodeConfigs))
		if len(c.NodeConfigs) != 20 {
			t.Fatalf("expected 20 nodes, got %d", len(c.NodeConfigs))
		}
	})
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

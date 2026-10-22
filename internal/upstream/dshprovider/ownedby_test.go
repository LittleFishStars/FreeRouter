package dshprovider

import (
	"encoding/json"
	"testing"
)

// TestContractModelOwnedByAuthority：hub 给 owned_by 时直用（权威值）——
// 01-catalog.md §2.3 的映射来源（`owned_by` → 派生渠道 → 限定名渠道段）。
func TestContractModelOwnedByAuthority(t *testing.T) {
	cases := []struct {
		name     string
		ownedBy  string
		want     string
		wantAuth bool
	}{
		{"hub 给了权威渠道", "deepseek-official", "deepseek-official", true},
		{"权威值与 id 前缀不一致时仍用权威值", "true-owner", "true-owner", true},
		{"空白权威值按未给处理（回落派生，标 Derived）", "   ", "deepseek-official", false},
	}
	for _, tc := range cases {
		m := ModelInfo{UpstreamModelID: "deepseek-official/deepseek-v4.1", OwnedBy: tc.ownedBy}
		got := m.ModelOwnedBy()
		if got.Channel != tc.want || got.Authoritative != tc.wantAuth {
			t.Errorf("%s：ModelOwnedBy()=%+v want channel=%q auth=%v", tc.name, got, tc.want, tc.wantAuth)
		}
		// 权威值与派生标记互斥（两者同时为真会让调用方无法判定可信度）。
		if got.Authoritative && got.Derived {
			t.Errorf("%s：权威值不应同时标 Derived", tc.name)
		}
		if !tc.wantAuth && !got.Derived {
			t.Errorf("%s：非权威结果必须标 Derived（空白 owned_by 回落派生）", tc.name)
		}
	}

	// 权威值优先级：id 前缀与 owned_by 不同也要以 owned_by 为准（不混用）。
	m := ModelInfo{UpstreamModelID: "aggregate/auto", OwnedBy: "deepseek-official"}
	got := m.ModelOwnedBy()
	if got.Channel != "deepseek-official" || !got.Authoritative || got.Derived {
		t.Fatalf("owned_by 必须压过 id 前缀派生：%+v", got)
	}
}

// TestContractModelOwnedByDerived：hub 不给时从 `渠道/模型` 前缀派生，
// 且**显式标 Derived**（派生值不是权威值——01-catalog.md §2.3）。
func TestContractModelOwnedByDerived(t *testing.T) {
	cases := []struct {
		name  string
		model string
		want  string
	}{
		{"标准前缀", "deepseek-official/deepseek-v4.1", "deepseek-official"},
		{"aggregate 前缀（01-catalog 7.2 排除面）", "aggregate/auto", "aggregate"},
		{"模型名本身含斜杠时只切第一段", "qodercn/org/model-v2", "qodercn"},
	}
	for _, tc := range cases {
		got := ModelInfo{UpstreamModelID: tc.model}.ModelOwnedBy()
		if got.Channel != tc.want {
			t.Errorf("%s：Channel=%q want %q", tc.name, got.Channel, tc.want)
		}
		if !got.Derived {
			t.Errorf("%s：派生值必须标 Derived", tc.name)
		}
		if got.Authoritative {
			t.Errorf("%s：派生值不得标 Authoritative", tc.name)
		}
	}
}

// TestContractModelOwnedByBareIDReturnsEmpty：裸 id（无 `/`）返回空串不 panic，
// 前导斜杠的畸形 id 同样不猜。
func TestContractModelOwnedByBareIDReturnsEmpty(t *testing.T) {
	bare := []string{"gpt-4o", "", "no-slash-here", "a/b/c", "/leading-slash"}
	for _, model := range bare {
		got := ModelInfo{UpstreamModelID: model}.ModelOwnedBy()
		switch model {
		case "a/b/c":
			// 只有首斜杠前是渠道：a/b/c → a（与 qodercn/org/model-v2 同规则）。
			if got.Channel != "a" || !got.Derived {
				t.Errorf("a/b/c 应派生 a：%+v", got)
			}
		case "/leading-slash":
			// 前导斜杠 = 畸形：不猜渠道（空串）。
			if got.Channel != "" || got.Derived || got.Authoritative {
				t.Errorf("前导斜杠应返回零值：%+v", got)
			}
		default:
			if got.Channel != "" || got.Derived || got.Authoritative {
				t.Errorf("裸 id %q 应返回零值（无渠道可派生）：%+v", model, got)
			}
		}
	}
}

// TestContractModelOwnedByWireDecode：wire 侧 ownedBy 缺席时保持空串——
// 不编造（hub 不给就是不给）。
func TestContractModelOwnedByWireDecode(t *testing.T) {
	// sidecar 回帧里没有 ownedBy 字段：解码应成功且 OwnedBy 为空。
	var m ModelInfo
	raw := `{"upstreamModelId":"buddy/deepseek-v4.1","displayName":"DS","contextWindow":200000,"modalities":["text"],"efforts":["high"]}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if m.OwnedBy != "" {
		t.Fatalf("hub 未给 ownedBy 时必须保持空（不得编造）：got %q", m.OwnedBy)
	}
	// 派生路径仍可用（标 Derived）。
	got := m.ModelOwnedBy()
	if got.Channel != "buddy" || !got.Derived || got.Authoritative {
		t.Fatalf("缺席时走派生并标 Derived：%+v", got)
	}

	// wire 给了 ownedBy：解码进字段。
	var m2 ModelInfo
	if err := json.Unmarshal([]byte(`{"upstreamModelId":"x/y","ownedBy":"real-owner"}`), &m2); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if m2.OwnedBy != "real-owner" {
		t.Fatalf("wire ownedBy 未解进字段：got %q", m2.OwnedBy)
	}
	if g := m2.ModelOwnedBy(); !g.Authoritative || g.Channel != "real-owner" {
		t.Fatalf("wire 权威值应直用：%+v", g)
	}
}

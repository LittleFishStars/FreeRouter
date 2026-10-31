// checkin_supported_test.go —— 签到能力清单的口径（2026-11-01）。
//
// 覆盖两条最容易写错的行为：
//  1. sidecar 未装配 → **空列表且不报错**（前端据此不渲染按钮；「不知道」
//     绝不能显示成「全部支持」）；
//  2. 清单为空是合法结果（不是错误），调用方拿到 []string{} 而不是 nil。
package app

import (
	"context"
	"testing"
)

// TestCheckinSupportedWithoutSidecar 钉住「没 sidecar 就报空清单」。
func TestCheckinSupportedWithoutSidecar(t *testing.T) {
	// sidecar 为 nil 的桥（与 sidecar 未就绪等价）。
	b := &checkinBridge{}
	list, err := b.CheckinSupportedProviders(context.Background())
	if err != nil {
		t.Fatalf("sidecar 未装配不应报错（前端少显示按钮即可）：%v", err)
	}
	if list == nil {
		t.Fatal("应返回空切片而不是 nil（前端按长度判断，nil 会多一个分支）")
	}
	if len(list) != 0 {
		t.Fatalf("sidecar 未装配应返回空清单，得 %v", list)
	}
}

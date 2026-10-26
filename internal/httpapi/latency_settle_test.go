package httpapi

import (
	"context"
	"testing"
)

// fakeLatencySink 记录收到的样本，验证 settle→sink 链路。
type fakeLatencySink struct {
	gotReal  string
	gotTTFT  int64
	gotTotal int64
	hit      bool
}

func (f *fakeLatencySink) RecordLatency(_ context.Context, realModelID string, ttftMs, totalMs int64) {
	f.gotReal = realModelID
	f.gotTTFT = ttftMs
	f.gotTotal = totalMs
	f.hit = true
}

func TestSettleFeedsLatencySink(t *testing.T) {
	s := &Server{}
	fake := &fakeLatencySink{}
	s.SetLatencySink(fake)

	// 造一个带 obs 的 settleState。
	st := &settleState{obs: attemptObs{RealModelID: "7", TTFTMs: 123, TotalMs: 456}}
	// settleRequest 需要 settler 才往下走；这里只验证 sink 在 settler==nil 时也触发。
	s.settleRequest(context.Background(), proxyReq{}, "req-1", st)
	if !fake.hit {
		t.Fatal("latency sink 应在 settler==nil 时也收到样本")
	}
	if fake.gotReal != "7" || fake.gotTTFT != 123 || fake.gotTotal != 456 {
		t.Fatalf("样本未正确传递：real=%s ttft=%d total=%d", fake.gotReal, fake.gotTTFT, fake.gotTotal)
	}
}

func TestSettleSkipsUnknownModel(t *testing.T) {
	s := &Server{}
	fake := &fakeLatencySink{}
	s.SetLatencySink(fake)
	// 未知行（RealModelID 为空串哨兵）的观测不应进采样器（无法归属到速排序）。
	s.settleRequest(context.Background(), proxyReq{}, "req-2", &settleState{obs: attemptObs{RealModelID: "", TTFTMs: 1, TotalMs: 2}})
	if fake.hit {
		t.Fatal("RealModelID==0 的观测不应喂给 sink")
	}
}

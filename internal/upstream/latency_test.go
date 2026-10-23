package upstream

import (
	"context"
	"testing"
	"time"
)

func TestLatencySamplerEWMA(t *testing.T) {
	s := NewLatencySampler(0.5) // α=0.5 方便心算
	key := SamplerKey{UpstreamID: 1, RealModel: "deepseek-v3"}
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	// 第一次观测初始化 EWMA（n==0 直接用该样本）。
	s.RecordSuccess(context.Background(), key, 100, 200, now)
	got := s.Latency(key)
	if got.TTFTMs != 100 || got.TotalMs != 200 {
		t.Fatalf("首次样本应直接采用：got ttft=%v total=%v，want 100/200", got.TTFTMs, got.TotalMs)
	}
	if !got.LastSample.Equal(now) {
		t.Fatalf("样本时刻未记录：%v", got.LastSample)
	}

	// 第二次 0.5:0.5 → ttft=150, total=150。
	now2 := now.Add(10 * time.Second)
	s.RecordSuccess(context.Background(), key, 200, 100, now2)
	got = s.Latency(key)
	if got.TTFTMs != 150 || got.TotalMs != 150 {
		t.Fatalf("EWMA 0.5:0.5 期望 150/150，got %v/%v", got.TTFTMs, got.TotalMs)
	}
	if !got.LastSample.Equal(now2) {
		t.Fatalf("样本时刻应更新到第二次：%v", got.LastSample)
	}
}

func TestLatencySamplerNoSample(t *testing.T) {
	s := NewLatencySampler(0)
	got := s.Latency(SamplerKey{UpstreamID: 9, RealModel: "x"})
	if got.TTFTMs != -1 || got.TotalMs != -1 {
		t.Fatalf("无样本应返回 -1 垫底，got %v/%v", got.TTFTMs, got.TotalMs)
	}
}

func TestLatencySamplerRejectsNegative(t *testing.T) {
	s := NewLatencySampler(0)
	key := SamplerKey{UpstreamID: 1, RealModel: "m"}
	now := time.Now()
	// 负值（-1 是无样本哨兵）不该进窗口。
	s.RecordSuccess(context.Background(), key, -1, 500, now)
	got := s.Latency(key)
	if got.TTFTMs != -1 || got.TotalMs != -1 {
		t.Fatalf("-1 哨兵不该进窗口，got %v/%v", got.TTFTMs, got.TotalMs)
	}

	// 合法值应能记录。
	s.RecordSuccess(context.Background(), key, 300, 400, now)
	got = s.Latency(key)
	if got.TTFTMs != 300 || got.TotalMs != 400 {
		t.Fatalf("合法样本应记录，got %v/%v", got.TTFTMs, got.TotalMs)
	}
}

func TestLatencySamplerKeyIsolation(t *testing.T) {
	s := NewLatencySampler(0)
	now := time.Now()
	s.RecordSuccess(context.Background(), SamplerKey{UpstreamID: 1, RealModel: "a"}, 100, 100, now)
	// 不同上游同一模型名不串。
	got := s.Latency(SamplerKey{UpstreamID: 2, RealModel: "a"})
	if got.TTFTMs != -1 {
		t.Fatalf("不同 upstream 的样本不应共享，got %v", got.TTFTMs)
	}
}

func TestLatencySamplerConcurrent(t *testing.T) {
	s := NewLatencySampler(0)
	key := SamplerKey{UpstreamID: 1, RealModel: "m"}
	done := make(chan struct{})
	now := time.Now()
	// 并发写读不应 panic / 数据竞争（-race 跑）。
	go func() {
		for i := 0; i < 1000; i++ {
			s.RecordSuccess(context.Background(), key, float64(i), float64(i), now)
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = s.Latency(key)
	}
	<-done
}
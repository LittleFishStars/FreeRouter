package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream/dshprovider"
)

// httpCatalog 是 vendor/selfhosted 的模型目录拉取（GET {base}/models）。
//
// 为什么需要它：sidecar 只认识 providers-hub 的 provider（relay）；贡献者交的
// API Key 与管理员自建的端点都是普通 HTTP 端点，目录得自己拉。没有这一步，
// 这两类上游永远进不了真实模型目录，抽象模型也就没有候选可选。
//
// 认证：密钥从 store 的密文解出来（AES-GCM），只在本次请求里用一次。
// 解不出来就报错（"凭据链路未接"），不静默发一个无鉴权请求。
func (a *App) fetchHTTPModels(ctx context.Context, u store.Upstream) ([]dshprovider.ModelInfo, error) {
	if u.Kind == "relay" {
		return nil, fmt.Errorf("relay 上游不走 HTTP 目录拉取")
	}
	base := strings.TrimRight(u.BaseURL, "/")
	if base == "" {
		return nil, fmt.Errorf("上游 %s 未配置 base_url", u.Name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	if u.SecretID != nil {
		secret, err := a.store.Repos().Secrets.GetSecret(ctx, *u.SecretID)
		if err != nil {
			return nil, fmt.Errorf("读取上游 %s 的密钥失败（凭据链路未接）: %w", u.Name, err)
		}
		if len(secret) > 0 {
			req.Header.Set("Authorization", "Bearer "+string(secret))
		}
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s/models 失败: %w", base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("上游 %s 拒绝鉴权（%d）：检查 api_key", u.Name, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("上游 %s 返回 %d", u.Name, resp.StatusCode)
	}

	// 兼容两种形状：{"data":[{"id":...}]}（OpenAI）与 [{"id":...}]（裸数组）。
	var wrapper struct {
		Data []struct {
			ID            string   `json:"id"`
			Name          string   `json:"name"`
			ContextWindow *int     `json:"context_window"`
			Modalities    []string `json:"modalities"`
			Efforts       []string `json:"efforts"`
		} `json:"data"`
	}
	raw, err := readAllLimited(resp, 4<<20)
	if err != nil {
		return nil, err
	}
	// 必须先解 OpenAI 形状；失败/为空再试裸数组（两种端点都常见）。
	_ = json.Unmarshal(raw, &wrapper)
	items := wrapper.Data
	if len(items) == 0 {
		var arr []struct {
			ID            string   `json:"id"`
			Name          string   `json:"name"`
			ContextWindow *int     `json:"context_window"`
			Modalities    []string `json:"modalities"`
			Efforts       []string `json:"efforts"`
		}
		if err := json.Unmarshal(raw, &arr); err == nil {
			items = arr
		}
	}
	out := make([]dshprovider.ModelInfo, 0, len(items))
	for _, it := range items {
		if it.ID == "" {
			continue
		}
		name := it.Name
		if name == "" {
			name = it.ID
		}
		out = append(out, dshprovider.ModelInfo{
			UpstreamModelID: it.ID,
			DisplayName:     name,
		})
	}
	return out, nil
}

func readAllLimited(resp *http.Response, limit int64) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	var total int64
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return nil, fmt.Errorf("上游目录响应超过 %d 字节", limit)
			}
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return nil, err
		}
	}
}

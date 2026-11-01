// frimport —— 把本机 DSH 的 OAuth 反代凭据导入 FreeRouter 库（一次性运维工具）。
//
// 为什么需要独立工具而不是走 HTTP 贡献接口：`POST /api/contributions` 的
// `resolveSubmissionTarget` 只接受 https（或回环 http）端点，而 relay 行的
// base_url 是定案值 `sidecar://<provider>`——渠道类型决定了它根本进不来那条
// 链路。而 OAuth 落库（app/oauth_codearts.go 的 persist）**只有**「走完一轮
// 真实扫码/设备码」这一条触发路径，没有对应的管理面端点。本机已有换好的
// 凭据原文，重跑授权只会换出一份新的、旧的也还在供应商那边。
//
// 所以这里直接调用**生产同一套** store 仓储，逐条复刻 oauth persist 的三步
// （UpsertUpstream → PutSecret → CreateCredential），不自己写加密、不自己
// 拼 SQL：口径一旦分叉，导进来的行就与真实授权产出的行长得不一样。
//
// 用法（在能访问库文件的机器上）：
//
//	go run ./cmd/frimport -db /path/freerouter.db -key /path/master.key \
//	    -in creds.json [-dry-run]
//
// creds.json 形状：[{"provider":"qoder","blob":"<凭据 JSON 原文>","label":"…"}]。
//
// 去重口径（与贡献链路同源）：UNIQUE(provider, external_id)，external_id 是
// KeyFingerprint(blob) = "key_" + sha256(blob)[:16]。已存在的 provider+fingerprint
// 一律跳过，不写任何行——重复导入是幂等的。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/LittleFishStars/FreeRouter/internal/store"
	"github.com/LittleFishStars/FreeRouter/internal/upstream"
)

// importRow 是一条待导入的凭据。
type importRow struct {
	Provider string `json:"provider"`
	Blob     string `json:"blob"`
	Label    string `json:"label"`
}

// relayProviders 是 sidecar 清单声明 kind=relay 的渠道（registry.mjs 的
// ENDPOINTS 表）。只有这些能落 sidecar:// 形态；其余（vendor 直连）走
// 别的通道，本工具不处理。
var relayProviders = map[string]bool{
	"accio": true, "autoclaw": true, "buddy": true, "workbuddy": true,
	"minimax": true, "minimaxintl": true, "qoder": true, "qodercn": true,
	"trae": true, "zcode": true, "gemini": true,
}

func main() {
	var (
		dbPath  = flag.String("db", "", "主库路径")
		keyPath = flag.String("key", "", "主密钥文件路径（0600）")
		inPath  = flag.String("in", "", "待导入 JSON 清单")
		owner   = flag.String("owner", "", "归属账户 id（UUID）；空 = 平台自有（NULL）")
		dryRun  = flag.Bool("dry-run", false, "只打印计划，不写库")
		quiet   = flag.Bool("quiet", false, "只输出最终汇总")
	)
	flag.Parse()
	if *dbPath == "" || *keyPath == "" || *inPath == "" {
		fmt.Fprintln(os.Stderr, "用法：frimport -db <库> -key <主密钥> -in <清单.json> [-owner <账户id>] [-dry-run]")
		os.Exit(2)
	}

	raw, err := os.ReadFile(*inPath)
	if err != nil {
		fatal("读清单失败", err)
	}
	var rows []importRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		fatal("解析清单失败", err)
	}

	ctx := context.Background()
	opts := store.Options{Path: *dbPath, MasterKeyFile: *keyPath, BusyTimeout: 5 * time.Second}
	st, err := store.Open(ctx, opts)
	if err != nil {
		fatal("打开库失败", err)
	}
	defer func() { _ = st.Close() }()

	// 已有凭据的 (provider, external_id) 集合，用于去重。
	existing := map[string]bool{}
	credRows, err := st.Repos().Models.ListCredentials(ctx, store.CredentialFilter{})
	if err != nil {
		fatal("读现有凭据失败", err)
	}
	for _, c := range credRows {
		existing[c.Provider+"\x00"+c.ExternalID] = true
	}

	var ownerPtr *string
	if strings.TrimSpace(*owner) != "" {
		o := strings.TrimSpace(*owner)
		ownerPtr = &o
	}

	var (
		imported, skipped, failed int
		failList                  []string
	)
	for i, r := range rows {
		prov := strings.TrimSpace(r.Provider)
		if prov == "" || strings.TrimSpace(r.Blob) == "" {
			failList = append(failList, fmt.Sprintf("#%d provider/blob 为空", i))
			failed++
			continue
		}
		if !relayProviders[prov] {
			failList = append(failList, fmt.Sprintf("%s：不是 relay 类渠道，本工具不处理", prov))
			failed++
			continue
		}
		fp := upstream.KeyFingerprint(r.Blob)
		if existing[prov+"\x00"+fp] {
			skipped++
			if !*quiet {
				fmt.Printf("跳过 %-12s %s（已存在）\n", prov, fp)
			}
			continue
		}
		// 上游命名与 oauth persist 一致：<provider>-oauth-<指纹前8>（指纹含
		// "key_" 前缀，故取 [4:12] 得 8 个十六进制字符）。
		name := fmt.Sprintf("%s-oauth-%s", prov, fp[4:12])
		// relay 行的 base_url 是定案值；store 的 UpsertUpstream 不做 kind 判定，
		// 这里显式给 sidecar:// 前缀（与 app 的 reconcile 口径同源）。
		baseURL := upstream.RelayBaseURLOf(prov)
		if *dryRun {
			imported++
			fmt.Printf("[dry-run] 将导入 %-12s %s -> %s（%d 字节）\n", prov, fp, name, len(r.Blob))
			continue
		}

		err := st.WithTx(ctx, func(tx *store.Repos) error {
			// OAuth 落库默认直接启用（供应商账号体系背书）：reconcile 会把它
			// 翻成 relay 形态，推理走 sidecar infer.stream。
			uid, uerr := tx.Models.UpsertUpstream(ctx, "relay", name, baseURL, ownerPtr, true)
			if uerr != nil {
				return fmt.Errorf("建上游行失败: %w", uerr)
			}
			sid, serr := tx.Secrets.PutSecret(ctx, uid, []byte(r.Blob))
			if serr != nil {
				return fmt.Errorf("写密文失败: %w", serr)
			}
			label := r.Label
			if label == "" {
				label = prov + " OAuth"
			}
			cid, cerr := tx.Models.CreateCredential(ctx, deref(ownerPtr), prov, fp, label, sid)
			if cerr != nil {
				// 回滚时把密文一起撤掉，避免留下无主的密钥材料（与 persist 同口径）。
				_ = tx.Secrets.DeleteSecret(ctx, sid)
				return fmt.Errorf("建凭据行失败: %w", cerr)
			}
			// OAuth 凭据直接入池：active。
			if err := tx.Models.SetCredentialStatus(ctx, cid, store.CredActive); err != nil {
				return fmt.Errorf("置 active 失败: %w", err)
			}
			return nil
		})
		if err != nil {
			failed++
			failList = append(failList, fmt.Sprintf("%s %s：%v", prov, fp, err))
			continue
		}
		imported++
		existing[prov+"\x00"+fp] = true
		if !*quiet {
			fmt.Printf("导入 %-12s %s -> %s\n", prov, fp, name)
		}
	}

	fmt.Printf("\n结果：导入 %d / 跳过(已存在) %d / 失败 %d（共 %d 条）\n",
		imported, skipped, failed, len(rows))
	for _, f := range failList {
		fmt.Println("  失败：", f)
	}
	if *dryRun {
		fmt.Println("（dry-run，未写入任何行）")
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func fatal(msg string, err error) {
	fmt.Fprintf(os.Stderr, "%s：%v\n", msg, err)
	if errors.Is(err, store.ErrSecretDecrypt) {
		fmt.Fprintln(os.Stderr, "提示：主密钥与库不匹配（密文解不开）。确认 -key 指向这份库对应的 master.key。")
	}
	os.Exit(2)
}

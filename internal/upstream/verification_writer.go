// 目录侧鉴定写回（task-3 追加点 / 巡检 [一般 7]）：VerificationWriter 的装配实现。
//
// 背景：real_models 的 verify_status/verify_detail/verify_source 三列（0004 迁移）
// 与 upstream.VerificationWriter 接口此前只有定义 + 测试 fake，没有任何真实写入方，
// 于是「凡在池中的行都至少过过一遍判定」在数据上不成立。本文件补上这唯一一条写路径。
//
// 归属判断（为什么适配层在 upstream 而不是 store）：
//   - 接口 VerificationWriter 与判定类型 VerifyState/JudgeRealModel 都属于上游域；
//     store 只认识自己的 VerifyStatus 与行 id，不该知道指纹库版本怎么拼。
//   - 类型翻译（VerifyState → store.VerifyStatus）与版本映射（int → "builtin_vN"）
//     同属「装配」职责。放在这里，装配层只需要一行 NewStoreVerificationWriter(store)。
package upstream

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/LittleFishStars/FreeRouter/internal/store"
)

// StoreVerificationWriter 把 JudgeRealModel 的判定写回 real_models 的目录侧三列。
//
// 为什么是窄结构体而不是让 *store.Store 直接实现 VerificationWriter：store 会被迫
// import upstream（依赖方向倒置，且 store 的契约测试会连同指纹表一起搭台）。
// 窄适配器只依赖仓储方法，测试可以用真 store + 临时库（见 verification_writer_test.go）。
type StoreVerificationWriter struct {
	store *store.Store
}

// NewStoreVerificationWriter 建写回器。st 允许为 nil——装配层在 M2 渐进接线时
// 仍可安全持有（每次 WriteVerification 返回结构化错误，刷新器记日志并继续）。
func NewStoreVerificationWriter(st *store.Store) *StoreVerificationWriter {
	return &StoreVerificationWriter{store: st}
}

// WriteVerification 实现 VerificationWriter。
//
// version 是判定所用指纹库版本（§6.2「判定可追溯」）；列存的是文本标识而不是数字，
// 映射由 VerifySourceOf 唯一负责。
func (w *StoreVerificationWriter) WriteVerification(ctx context.Context, realModelID int64, state VerifyState, evidence string, fingerprintVersion int) error {
	if w == nil || w.store == nil {
		return errors.New("upstream: 鉴定写回未装配 storage（WriteVerification）")
	}
	status, err := storeStatusOf(state)
	if err != nil {
		return err
	}
	if err := w.store.Repos().Models.SetRealModelVerification(
		ctx, realModelID, status, evidence, VerifySourceOf(fingerprintVersion)); err != nil {
		return fmt.Errorf("upstream: 写回 real_model %d 的鉴定结果失败: %w", realModelID, err)
	}
	return nil
}

// VerifySourceOf 把指纹库版本号映射成 verify_source 列的文本形态（如 builtin_v1）。
//
// 为什么必须集中在这里：列是 TEXT 且语义是「版本标识」，不是数字。直接写 "1" 会让
// 「哪些行是 v1 判的」与未来的其他来源（external_yaml/v2、人工 trusted）无法区分；
// 而如果每个装配点各写一遍 fmt.Sprintf，就会出现 builtin_v1 / v1 / 1 三种形态并存。
// 改格式时同步 0004 注释与本函数，不要在装配层另写。
func VerifySourceOf(fingerprintVersion int) string {
	return "builtin_v" + strconv.Itoa(fingerprintVersion)
}

// storeStatusOf 把上游判定四态翻译成存储层四态。
//
// 空串按 unverified：JudgeRealModel 对「无指纹的冷门模型」返回的就是 unverified，
// 空串只可能来自接线错误；按 unverified 处理是 05-verification §5.2 的默认放行
// 语义（宁可标未鉴定，也不要因接线漏传而把模型判成 suspect 拒之门外）。
func storeStatusOf(state VerifyState) (store.VerifyStatus, error) {
	switch state {
	case VerifyVerified:
		return store.VerifyVerified, nil
	case VerifySuspect:
		return store.VerifySuspect, nil
	case VerifyUnverified:
		return store.VerifyUnverified, nil
	case VerifyTrusted:
		return store.VerifyTrusted, nil
	case "":
		return store.VerifyUnverified, nil
	default:
		return "", fmt.Errorf("upstream: 非法鉴定状态 %q（四态：verified/suspect/unverified/trusted）", state)
	}
}

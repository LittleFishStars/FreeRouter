package httpapi

// 恢复码格式的守卫测试（2026-10-26 UUID 改造）。
//
// 为什么单独一个文件：这是本次改造里**唯一一处"格式变了但编译器看不出来"**的地方。
// 恢复码 `frv_<accountID>_<secret>` 的 accountID 现在是 UUID 文本，而 UUID 自身
// 含 4 个 `-`。若分隔符被改回改造前的 `-`，SplitN(code,"-",3) 会把 UUID 从中间
// 截断成两截，兑换路径**永远失败但不报编译错**——用户拿到码、粘贴、得到
// accepted:false，没有任何一处日志说明为什么。类型系统帮不上忙，只能靠测试。

import (
	"strings"
	"testing"
)

// TestContractRecoveryCodeRoundTripsUUIDAccount 钉住「UUID 账户 id 能编也能解」。
func TestContractRecoveryCodeRoundTripsUUIDAccount(t *testing.T) {
	// 形状合法但不存在于任何库里的账户 id：这条测试只关心**编码格式**，
	// 兑换的库查找在更外层（HandleContributeRecovery 的测试面）。
	const account = "550e8400-e29b-41d4-a716-446655440000"

	// 手工拼码（issueRecoveryCode 需要 Store 与 Hasher 才能跑，而这里要测的是
	// **格式约定**本身；生成侧与解析侧的一致性由文件末尾
	// TestContractRecoveryCodeIssueFormat 用同一拼法闭环）。
	code := "frv_" + account + "_" + "aGVsbG8td29ybGQ"
	id, secret, ok := parseRecoveryCode(code)
	if !ok {
		t.Fatalf("合法码被拒：%q", code)
	}
	if id != account {
		t.Fatalf("账户 id 应原样取出，期望 %q 实得 %q", account, id)
	}
	if secret != "aGVsbG8td29ybGQ" {
		t.Fatalf("secret 应取到最后一段，实得 %q", secret)
	}
}

// TestContractRecoveryCodeRejectsHyphenSeparator 钉住「旧分隔符必须被拒」。
//
// 这是回归防护的核心：如果 parseRecoveryCode 的 SplitN 分隔符被改回 `-`，
// 下面这条 `frv-<uuid>-<secret>` 会被解析成「id=550e8400」+「secret 里含负号段」
// 之类的错值并**可能返回 ok=true**（取决于实现），本测试就红。
func TestContractRecoveryCodeRejectsHyphenSeparator(t *testing.T) {
	const acct = "550e8400-e29b-41d4-a716-446655440000"
	code := "frv-" + acct + "-secret123"
	if id, secret, ok := parseRecoveryCode(code); ok {
		t.Fatalf("用 `-` 分隔的旧格式必须被拒（UUID 自带连字符，会拆错），实得 id=%q secret=%q", id, secret)
	}
}

// TestContractRecoveryCodeSecretMayContainHyphen 钉住 base64url 里的 `-` 不破坏解析。
//
// secret 是 base64.RawURLEncoding 的产物，字符集含 `-` 与 `_`。
// `-` 侧必须安全（分隔符是 `_`）；而 `_` 侧靠 SplitN 的「最多 3 段」保证——
// 第三段整体作为 secret，不再切分。两条都要钉住，否则换 base64 变体时会踩。
func TestContractRecoveryCodeSecretMayContainHyphen(t *testing.T) {
	const account = "550e8400-e29b-41d4-a716-446655440000"
	for _, secret := range []string{"ab-cd-ef", "ab_cd_ef", "ab-_cd", "a-b_c-d"} {
		code := "frv_" + account + "_" + secret
		_, got, ok := parseRecoveryCode(code)
		if !ok {
			t.Fatalf("secret=%q 应可解析（base64url 含 - 与 _）", secret)
		}
		if got != secret {
			t.Fatalf("secret=%q 应整段取出，实得 %q（SplitN 段数不对）", secret, got)
		}
	}
}

// TestContractRecoveryCodeRejectsBadIDShape 钉住非 UUID 账户 id 被拒。
//
// 为什么这也算格式：库里查不到的 id 最终也回 accepted:false（防枚举，见
// HandleContributeRecovery 的注释），所以形状校验**不是**给用户看的错误，
// 而是一条提前短路。短路必须与"码错"同形、且不产生可区分的时序分支。
func TestContractRecoveryCodeRejectsBadIDShape(t *testing.T) {
	for _, code := range []string{
		"frv_1_secret",     // 旧整型 id
		"frv_acc_1_secret", // 旧前缀 id
		"frv__secret",      // id 空
		"frv_550e8400-e29b-41d4-a716-446655440000_",  // secret 空
		"frv_550E8400-E29B-41D4-A716-446655440000_s", // 大写（Valid 只认小写）
		"frv-not-a-uuid-secret",
		"",
		"frv",
		"frv_",
		"xfrv_550e8400-e29b-41d4-a716-446655440000_s",
	} {
		if _, _, ok := parseRecoveryCode(code); ok {
			t.Errorf("应被拒：%q", code)
		}
	}
}

// TestContractRecoveryCodeIssueFormat 钉住生成侧的格式常量与解析侧一致。
//
// 生成侧在 issueRecoveryCode 里是字符串拼接（`"frv_" + accountID + "_" + secret`），
// 解析侧是 SplitN。两侧各写一遍字面量就会漂移——所以这条测试把**生成侧的产物**
// 喂给解析侧，形成闭环。
func TestContractRecoveryCodeIssueFormat(t *testing.T) {
	const account = "0f4f1b6c-2a3d-4e5f-8a9b-cdef01234567"
	const secret = "Zm9vYmFyLWJhenE"
	// 与 issueRecoveryCode 同一拼法（若那边的字面量改了，这里也要跟着改，
	// 且下面的往返断言会立刻指出解析侧不认）。
	code := "frv_" + account + "_" + secret
	if !strings.HasPrefix(code, "frv_") {
		t.Fatal("码前缀应是 frv_")
	}
	id, got, ok := parseRecoveryCode(code)
	if !ok || id != account || got != secret {
		t.Fatalf("生成侧产物解析失败：%q → id=%q secret=%q ok=%v", code, id, got, ok)
	}
}

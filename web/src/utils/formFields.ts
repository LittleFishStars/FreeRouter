// 非登录表单里的「秘密字段」与「伴随文本字段」的公共属性。
//
// ## 为什么需要这个文件（2026-10-25 实测）
//
// 贡献入口的「声称的模型」（文本）+「API Key」（password）在同一个 <form> 里，
// 浏览器据此判定这是一张**登录表单**：把保存的用户名自动填进「声称的模型」，
// 把保存的密码往 API Key 里塞，并在提交时弹出「保存密码？」。后果不只是难看：
// 贡献页的「声称的模型」是**会进鉴定判定的输入**，被填进一个假的模型名
// （截图里是 admin）就可能把一条真 Key 判成「声称与目录不符」→ suspect。
//
// ## 为什么是 new-password 而不是 off
//
// Chrome 的 autofill 文档明确写着：`autocomplete="off"` 对密码框**不生效**
// （只要它认为这是登录表单就照填不误），要拦下来得写 `new-password`——它的语义是
// 「这是一个新密码，不要去填已保存的凭据」。我们这里更进一步：这些根本**不是**密码，
// 是第三方 API Key，所以顺带把各家密码管理器（1Password / LastPass / Bitwarden /
// Proton Pass）的忽略标记一起挂上，它们认的是 data-* 而不是 autocomplete。
//
// ## 用法
//
//   <input type="password" {...SECRET_FIELD_PROPS} … />   // 第三方密钥、恢复码这类
//   <input {...PLAIN_FIELD_PROPS} … />                    // 与上面同表单的普通文本字段
//
// 别把 SECRET_FIELD_PROPS 用到**真登录表单**上（LoginPage 要的是 username /
// current-password，我们就是要浏览器在那里自动填）：这里解决的是
// 「不是登录、却被当成登录」这一类。
import type { InputHTMLAttributes } from 'react'

/**
 * data-* 属性不在 React 的 InputHTMLAttributes 类型里（DOM 规范的 data-* 是
 * 开放集合，React 的类型没为它们开口子）。这里补一层让两个常量都能被
 * spread 进 <input>，同时保留属性名的字面量类型——拼错 data-lpignore 这类
 * 魔法字符串时，错字会在这里被类型挡住，而不是静默失效。
 */
type DataAttrs = { readonly [K in `data-${string}`]?: string }
type FieldProps = InputHTMLAttributes<HTMLInputElement> & DataAttrs

/** 秘密字段：掩码显示、禁止任何密码管理器介入。 */
export const SECRET_FIELD_PROPS = {
  autoComplete: 'new-password',
  spellCheck: false,
  // 1Password / LastPass / Bitwarden / Proton Pass 的逐字段忽略标记。
  'data-1p-ignore': '',
  'data-lpignore': 'true',
  'data-bwignore': '',
  'data-protonpass-ignore': '',
  // 1Password 的「这是哪类表单」提示（other = 不要按登录表单处理）。
  'data-form-type': 'other'
} as const satisfies FieldProps

/**
 * 与秘密字段同表单的普通文本字段：同样要躲开用户名启发式。
 *
 * 为什么不能只给密码框加标记：Chrome 判定「哪个字段是用户名」看的是
 * 「密码框之前的那个文本字段」，只处理密码框会把用户名填到文本字段上。
 */
export const PLAIN_FIELD_PROPS = {
  autoComplete: 'off',
  'data-form-type': 'other'
} as const satisfies FieldProps

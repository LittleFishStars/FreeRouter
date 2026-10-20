// 登录页占位：公共区里唯一有独立形态的页面（无一级导航、独立卡片布局）。
// 真实表单（含退避与锁定提示）在 M9 接入后端契约后实现。

import type { PageProps } from '../routes'

export default function LoginPage({ def }: PageProps) {
  return (
    <section className="login-page">
      <h1>{def.title}</h1>
      <p className="page-summary">{def.summary}</p>
      <form className="login-card" onSubmit={(e) => e.preventDefault()}>
        <label>账户 ID 或邮箱 <span aria-hidden="true">*</span></label>
        <input name="identity" autoComplete="username" />
        <label>口令 <span aria-hidden="true">*</span></label>
        <input name="password" type="password" autoComplete="current-password" />
        <button type="submit" disabled>登录（后端未接入）</button>
      </form>
    </section>
  )
}

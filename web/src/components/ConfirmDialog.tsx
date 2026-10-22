// 危险操作的统一二次确认框（docs/ui/03-admin.md 1.3 / 06-design.md 第 5 节）。
//
// 四条共同要求：
//   1. 列出影响面（受影响的账户 / 组 / Key / 候选数量）；
//   2. 准确描述不可逆程度（「可恢复」与「不可恢复」必须区分）；
//   3. 输入目标资源的名称或 ID（不是勾选「我确认」）；
//   4. 确认按钮文案是具体动作（「确认停用 smart」），不是「确定」。
//
// 边界：它拦的是误点，不是身份。认证只发生在登录（docs/security/02-authn.md
// 第 6 节）——框里不重输密码，也不验证任何凭据。

import { useEffect, useId, useRef, useState } from 'react'

export interface ConfirmDialogProps {
  open: boolean
  /** 标题写具体动作，例如「确认停用抽象模型 smart」。 */
  title: string
  /** 影响面：逐条列出。 */
  impact: string[]
  /** 不可逆性描述：可恢复 / 期间请求已失败 / 不可恢复，必须说清。 */
  irreversibility: string
  /** 要求输入的目标名称或 ID。 */
  targetName: string
  /** 输入框上方的说明，默认「请输入目标名称 X 以确认」。 */
  inputLabel?: string
  /** 具体动作文案，例如「确认停用」。 */
  confirmLabel: string
  busy?: boolean
  error?: unknown
  onCancel: () => void
  onConfirm: () => void
}

export default function ConfirmDialog({
  open,
  title,
  impact,
  irreversibility,
  targetName,
  inputLabel,
  confirmLabel,
  busy,
  error,
  onCancel,
  onConfirm
}: ConfirmDialogProps) {
  const [typed, setTyped] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const titleId = useId()
  const descId = useId()

  // 每次打开重置输入并聚焦：焦点移入、关闭后回到触发元素是键盘可达要求
  // （06-design.md 第 8 节）。触发元素的恢复由调用方用原生 button 语义承接。
  useEffect(() => {
    if (open) {
      setTyped('')
      const t = window.setTimeout(() => inputRef.current?.focus(), 0)
      return () => window.clearTimeout(t)
    }
    return undefined
  }, [open])

  // 打开期间锁滚动；Escape 关闭（遮罩点击刻意不关闭——危险操作必须显式取消）。
  useEffect(() => {
    if (!open) return undefined
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !busy) onCancel()
    }
    window.addEventListener('keydown', onKey)
    return () => {
      document.body.style.overflow = prev
      window.removeEventListener('keydown', onKey)
    }
  }, [open, busy, onCancel])

  if (!open) return null

  const matched = typed.trim() === targetName
  const errMsg = error instanceof Error ? error.message : error ? String(error) : null

  return (
    <div className="dialog-overlay">
      <div
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={descId}
      >
        <h2 id={titleId}>{title}</h2>
        <div id={descId} className="dialog-body">
          <div className="dialog-impact">
            <p className="dialog-label">影响面</p>
            <ul>
              {impact.map((line, i) => (
                <li key={i}>{line}</li>
              ))}
            </ul>
          </div>
          <p>
            <span className="dialog-label">不可逆性：</span>
            {irreversibility}
          </p>
          <label className="field">
            <span className="field-label">
              {inputLabel ?? `请输入目标名称 ${targetName} 以确认`}
            </span>
            <input
              ref={inputRef}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              placeholder={targetName}
            />
            <span className="field-hint">
              输入必须与 <code>{targetName}</code> 完全一致；这是为了防止在多个相似对象之间点错。
            </span>
          </label>
          {errMsg && (
            <p className="form-error" role="alert">
              {errMsg}
            </p>
          )}
        </div>
        <div className="dialog-actions">
          <button type="button" className="btn-ghost" onClick={onCancel} disabled={busy}>
            取消
          </button>
          <button
            type="button"
            className="btn-danger"
            onClick={onConfirm}
            disabled={!matched || busy}
          >
            {busy ? '正在提交…' : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}

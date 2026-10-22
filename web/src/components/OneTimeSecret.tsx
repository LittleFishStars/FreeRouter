// 一次性秘密展示（API Key 明文、恢复码、重置密码）。
//
// docs/ui/06-design.md 3.3 的四条规则在这里统一实现：
//   1. 提交后整页替换为一次性展示视图（不是弹窗）；
//   2. 提供复制与下载；
//   3. 必须显式勾选「我已保存」才能离开；
//   4. 不提供任何形式的「再次显示」。
//
// API Key 与恢复码共用它，用户只学一次，实现只有一处需要保证正确。

import { useState } from 'react'
import { downloadText } from '../utils/format'
import { Banner } from './ui'

export interface OneTimeSecretProps {
  title: string
  secret: string
  /** 额外说明（例如「该账户已获得贡献者能力」）。 */
  description?: string
  /** 下载文件名，例如 api-key-fr-xxxx.txt */
  filename: string
  /** 勾选并点击完成后的去向。 */
  onDone: () => void
  doneLabel?: string
}

export default function OneTimeSecret({
  title,
  secret,
  description,
  filename,
  onDone,
  doneLabel = '完成'
}: OneTimeSecretProps) {
  const [copied, setCopied] = useState(false)
  const [saved, setSaved] = useState(false)
  const [copyError, setCopyError] = useState<string | null>(null)

  async function copy() {
    setCopyError(null)
    try {
      await navigator.clipboard.writeText(secret)
      setCopied(true)
    } catch {
      setCopyError('复制失败（浏览器可能限制了剪贴板权限），请手动选中复制或下载文件。')
    }
  }

  function download() {
    downloadText(filename, `${secret}\n`)
  }

  return (
    <div className="one-time-secret">
      <h1>{title}</h1>
      <Banner tone="warn" title="只显示这一次">
        离开本页后将无法再次查看。服务端只保存哈希，任何页面（包括管理端）都不会再显示明文。
      </Banner>
      {description && <p>{description}</p>}
      <div className="secret-box">
        <code>{secret}</code>
      </div>
      <div className="secret-actions">
        <button type="button" onClick={copy}>
          {copied ? '已复制' : '复制'}
        </button>
        <button type="button" className="btn-ghost" onClick={download}>
          下载为 .txt
        </button>
      </div>
      {copyError && (
        <p className="form-error" role="alert">
          {copyError}
        </p>
      )}
      <label className="checkbox-line">
        <input type="checkbox" checked={saved} onChange={(e) => setSaved(e.target.checked)} />
        我已保存好这个秘密（未勾选时不能离开）
      </label>
      <button type="button" className="btn-primary" disabled={!saved} onClick={onDone}>
        {doneLabel}
      </button>
    </div>
  )
}

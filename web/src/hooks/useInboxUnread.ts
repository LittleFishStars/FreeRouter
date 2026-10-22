// 待处理未读计数：全站唯一的轮询（docs/ui/03-admin.md 12.4）。
//
// 规则：
//   - 登录时拉一次；
//   - 页面打开期间每 60 秒轮询一次；
//   - 窗口重新获得焦点时立即刷一次；
//   - 不做 WebSocket / SSE（时效要求是分钟级，长连接买不到东西）。
//
// 其余页面一律不轮询（06-design.md 第 10 节），看到的数据以「最近一次拉取」为准。

import { useCallback, useEffect, useRef, useState } from 'react'
import { getInboxCount } from '../api/endpoints'

export function useInboxUnread(enabled: boolean): { unread: number; refresh: () => void } {
  const [unread, setUnread] = useState(0)
  const alive = useRef(true)

  const refresh = useCallback(() => {
    if (!enabled) return
    getInboxCount()
      .then((r) => {
        if (alive.current) setUnread(r.unread ?? 0)
      })
      .catch(() => {
        // 计数是装饰：后端未接线/暂时不可达时不影响导航，保持旧值
      })
  }, [enabled])

  useEffect(() => {
    alive.current = true
    if (!enabled) {
      setUnread(0)
      return () => {
        alive.current = false
      }
    }
    refresh()
    const timer = window.setInterval(refresh, 60_000)
    const onFocus = () => refresh()
    window.addEventListener('focus', onFocus)
    return () => {
      alive.current = false
      window.clearInterval(timer)
      window.removeEventListener('focus', onFocus)
    }
  }, [enabled, refresh])

  return { unread, refresh }
}

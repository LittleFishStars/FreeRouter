// 轻量数据获取 hook。
//
// 为什么不用 TanStack Query：前端依赖清单（web/package.json）刻意保持最小，
// 离线环境不能保证 pnpm 能装新包；本项目的取数需求也只需要
// 「加载态 / 错误态 / 重试 / 首次加载」四件事。docs/ui/06-design.md 第 10 节
// 把 TanStack Query 列为实现级建议，不是验收项——这里用 30 行等价实现，
// staleTime 语义退化为「每次挂载拉一次」，与文档「其余页面不轮询」一致。
//
// 轮询只有一处：管理端待处理未读数（useInboxUnread，60s + focus 立即刷）。

import { useCallback, useEffect, useRef, useState } from 'react'

export interface AsyncResult<T> {
  data: T | undefined
  error: unknown
  loading: boolean
  reload: () => void
  setData: (v: T | undefined) => void
}

/**
 * useApi：挂载时执行一次 loader，返回四态。
 * deps 变化会重新拉取；reload() 手动重拉（错误态的「重试」、写后刷新）。
 */
export function useApi<T>(loader: () => Promise<T>, deps: unknown[] = []): AsyncResult<T> {
  const [data, setData] = useState<T | undefined>(undefined)
  const [error, setError] = useState<unknown>(undefined)
  const [loading, setLoading] = useState(true)
  const [tick, setTick] = useState(0)
  const alive = useRef(true)
  // loader 每次渲染都是新函数，不能进依赖；由调用方的 deps 显式声明变化点。
  const loaderRef = useRef(loader)
  loaderRef.current = loader

  useEffect(() => {
    alive.current = true
    setLoading(true)
    setError(undefined)
    loaderRef
      .current()
      .then((v) => {
        if (alive.current) {
          setData(v)
          setLoading(false)
        }
      })
      .catch((e) => {
        if (alive.current) {
          setError(e)
          setLoading(false)
        }
      })
    return () => {
      alive.current = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick])

  const reload = useCallback(() => setTick((n) => n + 1), [])
  return { data, error, loading, reload, setData }
}

/**
 * 写操作状态机：页面所有提交按钮共用。
 * run() 失败时把错误留在 error 上（表单不回滚，docs/ui/06-design.md 3.2）。
 */
export function useMutation<TArgs extends unknown[], TResult>(
  fn: (...args: TArgs) => Promise<TResult>
) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(undefined)
  const [last, setLast] = useState<TResult | undefined>(undefined)

  const run = useCallback(
    async (...args: TArgs): Promise<TResult | undefined> => {
      setBusy(true)
      setError(undefined)
      try {
        const r = await fn(...args)
        setLast(r)
        return r
      } catch (e) {
        setError(e)
        return undefined
      } finally {
        setBusy(false)
      }
    },
    // fn 通常是内联箭头函数；调用方用 useCallback 固定时才能稳定。
    [fn]
  )

  return { run, busy, error, last, setError }
}

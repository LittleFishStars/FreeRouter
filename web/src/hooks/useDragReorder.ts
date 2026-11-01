// useDragReorder —— 横向芯片条/列表的指针拖动排序（2026-11-01）。
//
// 为什么自己写而不是引第三方 dnd 库：
//   1. 我们只需要「横向一维重排 + 指针事件」这一件事，dnd-kit/react-dnd 的
//      体积（几十 KB）与它的 sensor/collision 抽象都用不上；
//   2. 交互要求很具体且与视觉强绑定（拖动时芯片实时让位、被拖的芯片浮起
//      带阴影）——这些都要贴着我们的填充色阶体系写，库的默认表现反而要拆。
//
// ## 关键设计：用指针事件而不是 HTML5 dragstart
//
// HTML5 的 drag-and-drop 有三个在我们这里确实会出问题的缺陷：
//   - 触摸设备（平板上的管理台）不支持，dragstart 根本不会触发；
//   - 拖影由浏览器绘制，无法按我们的圆角/填充色阶风格定制，拖起来像
//     「另一个东西」而不是「这张芯片浮起来了」；
//   - dragover 的坐标语义依赖浏览器，横向条上跨元素判定很飘。
//
// 指针事件 + 自己算落点，三处都能控住；键盘用户仍有 ↑/↓ 按钮可用
// （本 hook 只负责指针路径，不夺走按钮——CandidateDialog 里两个路径并存）。
//
// ## 「实时预览」而不是「落点指示线」
//
// pointermove 时就把列表重排成预览序（被拖的芯片跟着指针走、其余芯片实时
// 让位），松手才提交。为什么不做「插入位置竖线」：横向条里芯片宽窄不一，
// 竖线的落点歧义（插在 A 之前还是之后）正是这类交互最常被抱怨的点；让
// 芯片自己让位则没有歧义——所见即所得。
//
// ## 为什么落点用「中心线比较」
//
// 用 elementFromPoint 或边界包含会在「拖到两个芯片之间的空隙」时给出
// undefined；改成「遍历容器内所有芯片，指针越过谁的水平中心就算越过谁」
// 永远有确定答案，且落点随指针移动单调变化。
//
// ## 隔离
//
// 查询只在本 hook 的容器 ref 内进行（containerRef）：抽象模型页的展开行
// 芯片条与候选编辑弹窗可能**同时**打开，全局查 [data-drag-key] 会把两个
// 容器的芯片混进同一次落点计算（实测会跨容器跳位）。

import { useEffect, useRef, useState, type RefObject } from 'react'

export interface DragReorderHandle {
  /** 挂到滚动容器（.am-chip-row / .amd-cur-row）上：限定拖动的查询范围。 */
  containerRef: RefObject<HTMLDivElement | null>
  /** 挂到每个可拖元素根节点。 */
  itemProps: (key: string, index: number) => {
    onPointerDown: (e: React.PointerEvent) => void
    style: React.CSSProperties
    'data-drag-key': string
    'data-dragging': true | undefined
  }
  /** 当前被拖的键（null = 没在拖）；驱动芯片的浮起样式。 */
  draggingKey: string | null
}

export interface UseDragReorderOptions {
  /**
   * 拖动中的实时预览：把 from 位元素挪到 to 位。调用方用 moveItem 生成
   * 新顺序并 setState——列表随即按预览序渲染。
   */
  onPreview: (from: number, to: number) => void
  /** 松手回调。moved=false 表示没有实际换位（原地点了一下）。 */
  onEnd: (moved: boolean) => void
  /** 拖动阈值（像素）：低于它不算拖动，让「点一下」仍然是一次点击。 */
  threshold?: number
}

/**
 * useDragReorder 返回容器 ref、每项挂载属性与拖动状态。
 *
 * 为什么阈值是必需的：芯片本身是个按钮（点它打开候选编辑器）。没有阈值时
 * 一次轻微的按下抖动就会被判成拖动，点击被吞掉——那是这类交互最常见的 bug。
 */
export function useDragReorder({ onPreview, onEnd, threshold = 4 }: UseDragReorderOptions): DragReorderHandle {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const [draggingKey, setDraggingKey] = useState<string | null>(null)

  // 拖动会话的临时数据（起点、起点索引、是否已开始）——
  // 放 ref 而不是 state：pointermove 每帧都写，用 state 会把整条芯片条
  // 每帧重渲染两次（预览序 + 拖动态），几十个芯片时肉眼可见地卡。
  const session = useRef<{
    key: string
    fromIndex: number
    startX: number
    started: boolean
    // 拖动**开始时**各芯片的几何快照（按初始顺序）。
    //
    // 为什么必须冻结：用实时 getBoundingClientRect 会陷入自我振荡——预览立刻
    // 重排，被拖的芯片跟着挪走，下一帧中心线比较又得反过来，于是顺序在两三个
    // 状态间来回跳（2026-11-01 实测：x 走一步就换一次顺序）。
    // 冻结初始几何后，落点只取决于指针位置，单调且可预测。
    rects: { key: string; center: number }[]
  } | null>(null)

  // 用 ref 镜像回调：onPreview/onEnd 每次渲染都是新函数（内联箭头），
  // 若直接进 effect 依赖，预览一变监听就重挂——丢指针事件的经典来源。
  const cbs = useRef({ onPreview, onEnd })
  cbs.current = { onPreview, onEnd }

  useEffect(() => {
    if (draggingKey === null) return
    const key = draggingKey

    const onMove = (e: PointerEvent) => {
      const s = session.current
      if (!s || s.key !== key) return
      // 阈值内不算拖动（点击保护）。
      if (!s.started && Math.abs(e.clientX - s.startX) < threshold) return
      s.started = true

      // 落点：容器内所有芯片，指针越过谁的水平中心就算越过谁。
      // 被拖的那颗跳过（它此刻跟着指针走，自身位置没有意义）。
      // 落点用「不含被拖项」的索引空间（moveItem 的 to 就是这个语义：
      // 先 splice 取出 from，再插到 to——此时数组里已经没有它了）。
      //
      // 两条关键规则：
      //  1. 越过某颗的中心 = **插到它之后**，所以 to = seen + 1（写成 seen 会
      //     让「拖到最右」永远止步在倒数第二位）。
      //  2. 几何用**拖动开始时冻结的快照**（s.rects），不是实时查询——
      //     实时查询会自我振荡（预览重排 → 被拖芯片挪走 → 落点反转）。
      let to = 0
      let seen = 0
      for (const r of s.rects) {
        if (r.key === key) continue // 跳过被拖的那颗
        if (e.clientX > r.center) to = seen + 1
        seen++
      }
      if (to > seen) to = seen
      if (to < 0) to = 0
      cbs.current.onPreview(s.fromIndex, to)
    }

    const finish = () => {
      const s = session.current
      session.current = null
      setDraggingKey(null)
      cbs.current.onEnd(Boolean(s && s.started))
    }
    const cancel = () => {
      session.current = null
      setDraggingKey(null)
      cbs.current.onEnd(false)
    }

    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', finish)
    window.addEventListener('pointercancel', cancel)
    // 拖动期间禁止浏览器把指针手势当成滚动/长按菜单：否则横向条在触屏上
    // 会先把整条滚走，拖动永远不成立。
    document.body.style.touchAction = 'none'
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', finish)
      window.removeEventListener('pointercancel', cancel)
      document.body.style.touchAction = ''
    }
  }, [draggingKey, threshold])

  function itemProps(key: string, index: number) {
    return {
      onPointerDown: (e: React.PointerEvent) => {
        // 只响应主键（鼠标左键 / 单指）。右键与中键留给系统菜单。
        if (e.button !== 0) return
        // 落点在**芯片内部**的交互控件上时不做拖动：
        // 弹窗芯片是「卡片里嵌着 ↑/↓/删除 按钮」的复合结构，不拦这一道会让
        // 「点一下上移」变成「开始拖动」，按钮再也点不动（2026-11-01 实测）。
        //
        // 判据是「最近的可交互祖先是否就是本源节点」：
        //   - 列表芯片的根**本身**是 <button data-drag-key> → 最近控件 = 自己 → 放行；
        //   - 弹窗卡片的根是 <div data-drag-key>，里面的 ↑ 按钮是它的后代 →
        //     最近控件 = 那个按钮（≠ 自己）→ 拦下。
        // 这样两种结构用同一条规则覆盖，不需要知道谁是 button 谁是 div。
        const el = e.target as HTMLElement | null
        const interactive = el?.closest('button, a, input, select, textarea, [data-no-drag]')
        if (interactive && interactive !== (e.currentTarget as HTMLElement)) return
        // 冻结当前几何（见 session.rects 注释：不冻结会自我振荡）。
        const root = containerRef.current
        const rects = root
          ? [...root.querySelectorAll<HTMLElement>('[data-drag-key]')].map((n) => {
              const r = n.getBoundingClientRect()
              return { key: n.getAttribute('data-drag-key') ?? '', center: r.left + r.width / 2 }
            })
          : []
        session.current = { key, fromIndex: index, startX: e.clientX, started: false, rects }
        setDraggingKey(key)
      },
      style: { cursor: draggingKey === key ? 'grabbing' : 'grab' } as React.CSSProperties,
      'data-drag-key': key,
      'data-dragging': draggingKey === key || undefined
    }
  }

  return { containerRef, itemProps, draggingKey }
}

/** moveItem 把 from 位元素挪到 to 位（纯函数，返回新数组；越界原样返回）。
 *
 * to 语义与 Array.prototype.splice 的插入位一致：先取出 from，再插到 to。
 * 「不含被拖项的插入位次」在这里换算：目标位在原位之后时，取出自身后
 * 后续元素左移一位，插入位次天然对齐，无需调用方再做 ±1 换算。
 */
export function moveItem<T>(list: T[], from: number, to: number): T[] {
  if (from === to || from < 0 || to < 0 || from >= list.length || to > list.length) return list
  const next = [...list]
  const [item] = next.splice(from, 1)
  next.splice(to, 0, item)
  return next
}

// 深浅色模式的上下文（与 main.tsx 分开一个文件，避免 react-refresh 警告：
// 同一文件里既导出组件又导出 context 会让 Fast Refresh 放弃整块模块）。

import { createContext, useContext } from 'react'

export type ColorMode = 'light' | 'dark'

export interface ColorModeState {
  mode: ColorMode
  toggle: () => void
}

export const ColorModeContext = createContext<ColorModeState>({
  mode: 'light',
  toggle: () => {}
})

/** 取当前模式与切换函数（顶栏的主题按钮用）。 */
export function useColorMode(): ColorModeState {
  return useContext(ColorModeContext)
}

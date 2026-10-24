// 入口：挂载 React 根，并装上 MUI 主题与深浅色切换。
//
// 为什么自己写 ColorMode 上下文而不直接用 MUI 的 `useColorScheme`：
// 本项目用两个 `createTheme` 实例（light/dark）而不是单个 CSS 变量主题——
// v9 的 cssVariables 方案要求把两套 palette 写进同一个主题的 `colorSchemes`，
// 那会让「某个组件在深色下要单独调色」变得很绕（得写层级选择器）。
// 两个主题实例 + 一次 state 切换更直白，代价只是切换时重渲染整棵树
// （对控制台这种体量完全可接受）。
//
// 偏好保存顺序：localStorage → 系统 prefers-color-scheme → 浅色。
// 不读 Cookie：这是纯前端偏好，不该为它多一个往返。

import { StrictMode, useCallback, useEffect, useMemo, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { CssBaseline, ThemeProvider } from '@mui/material'
import App from './App'
import { darkTheme, lightTheme, MODE_STORAGE_KEY } from './theme'
import { ColorModeContext, type ColorMode } from './colorMode'
import './style.css'

/** 读出初始模式：显式选择优先，否则跟随系统。 */
function initialMode(): ColorMode {
  try {
    const saved = localStorage.getItem(MODE_STORAGE_KEY)
    if (saved === 'light' || saved === 'dark') return saved
  } catch {
    // 隐私模式下 localStorage 可能抛错——静默退到系统偏好
  }
  if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: dark)').matches) {
    return 'dark'
  }
  return 'light'
}

function Root() {
  const [mode, setMode] = useState<ColorMode>(initialMode)

  // 把模式写到 <html> 上：style.css 与原生控件样式靠这个属性命中，
  // 避免每个组件都去读 context。
  useEffect(() => {
    document.documentElement.dataset.theme = mode
    document.documentElement.style.colorScheme = mode
    try {
      localStorage.setItem(MODE_STORAGE_KEY, mode)
    } catch {
      /* 存不进去就算了，不影响本次会话 */
    }
  }, [mode])

  const toggle = useCallback(() => setMode((m) => (m === 'light' ? 'dark' : 'light')), [])
  const value = useMemo(() => ({ mode, toggle }), [mode, toggle])
  const theme = mode === 'dark' ? darkTheme : lightTheme

  return (
    <ThemeProvider theme={theme}>
      <ColorModeContext.Provider value={value}>
        {/* CssBaseline 负责 reset + 把 body 背景设成 palette.background.default；
            没有它深色模式下 body 仍是白色，滚动到边缘会露白。 */}
        <CssBaseline enableColorScheme />
        <App />
      </ColorModeContext.Provider>
    </ThemeProvider>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Root />
  </StrictMode>
)

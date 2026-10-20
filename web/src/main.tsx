// 入口：挂载 React 根。样式刻意用最小组合——设计规范会在 M9 接入组件库时替换这里。
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import './style.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>
)

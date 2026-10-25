// 登录页：密码登录；错误文案遵循防枚举原则——
// 401 一律「用户名或口令错误」，不区分「用户不存在」（后端 LoginService
// 已把两条分支收敛为同码同文案同工作量，前端不得再区分开）。
// 429 锁定提示 + Retry-After 剩余秒数。
//
// ## 视觉要点（MUI 迁移）
//
// 登录页是**产品的第一印象**，但它只有一个任务：让运维少花时间。所以：
//   - 卡片垂直居中于视口剩余空间，而不是贴在顶部（顶部贴边会让页面显得没做完）；
//   - 标签用 MUI TextField 的浮动 label，而不是「标签 + 输入框挤一行」——
//     后者是原生 `<label>包裹 input` 的默认流式布局，在宽屏上会拉出一条长缝；
//   - 只有一个主操作按钮，且占满卡片宽度（手机上更容易点中）。

import { useState, type FormEvent } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router-dom'
import { Alert, Box, Button, Stack, TextField, Typography } from '@mui/material'
import LoginIcon from '@mui/icons-material/Login'
import { ApiError, postAuthLogin, type Session } from '../api/client'
import { useSession } from '../api/session'

export default function LoginPage() {
  const { session, setSession } = useSession()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [lockSeconds, setLockSeconds] = useState<number | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const navigate = useNavigate()
  const location = useLocation()
  // 初始化向导完成后跳过来的场合，给一句确认文案
  const justInited = (location.state as { inited?: boolean } | null)?.inited === true
  // 改密完成回登录页：提示用新口令登录（首启强制改密与普通改密共用）。
  const passwordChanged =
    (location.state as { passwordChanged?: boolean } | null)?.passwordChanged === true
  // 登录后回跳目标：守卫记录的原路径优先；强制改密优先于回跳。
  //
  // 只接受站内路径且**不能是 /login 自身**：history 里一旦残留
  // from='/login'（旧版 401 处理器在登录页上记录过），已登录时
  // <Navigate to={from}> 会把页面跳回登录页，而登录页又跳一次——
  // 无限跳转（实测表现就是页面闪烁后空白）。脏 state 一律回首页。
  const rawFrom = (location.state as { from?: string } | null)?.from
  const from = rawFrom && rawFrom.startsWith('/') && rawFrom !== '/login' ? rawFrom : '/'
  const mustChange = session?.must_change_password === true
  const redirectTo = mustChange ? '/account/password' : from

  if (session) return <Navigate to={redirectTo} replace /> // 已登录不再展示登录表单

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      const s: Session = await postAuthLogin({ username, password })
      setSession(s)
      // 首启随机口令/管理员重置：直接去改密页，不回到原路径。
      navigate(s.must_change_password ? '/account/password' : from, {
        replace: true,
        state: s.must_change_password ? { firstLogin: true } : undefined
      })
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.code === 'invalid_credentials') {
          setError('用户名或口令错误') // 防枚举：不区分用户是否存在
        } else if (err.code === 'too_many_attempts') {
          setLockSeconds(err.retryAfterSeconds ?? null)
          setError('尝试次数过多，账户已临时锁定')
        } else {
          setError(err.message)
        }
      } else {
        setError('登录失败，请重试')
      }
    } finally {
      setSubmitting(false)
    }
  }

  return (
    // minHeight 用视口高度减去顶栏(58)与页脚(约 46)，让卡片在剩余空间里居中
    <Box
      sx={{
        minHeight: 'calc(100vh - 180px)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        py: 4
      }}
    >
      <Box sx={{ width: '100%', maxWidth: 400 }}>
        <Box sx={{ mb: 3 }}>
          <Typography variant="h1" component="h1" sx={{ mb: 0.5 }}>
            登录
          </Typography>
          <Typography variant="body2" color="text.secondary">
            密码登录；会话为 HttpOnly Cookie，浏览器自动管理。
          </Typography>
        </Box>

        {justInited && (
          <Alert severity="success" sx={{ mb: 2 }}>
            初始化完成——请用刚创建的管理员账户登录。
          </Alert>
        )}
        {passwordChanged && (
          <Alert severity="info" sx={{ mb: 2 }}>
            口令已更新，请用新口令重新登录。
          </Alert>
        )}

        {/* 表单卡：靠「比页面亮一档」站起来，不画边框；它是浮在页面中央的
            单独一块，所以允许一层 e1 阴影（列表里的卡片不加）。 */}
        <Box
          component="form"
          onSubmit={onSubmit}
          sx={{
            display: 'flex',
            flexDirection: 'column',
            gap: 2,
            p: 3,
            borderRadius: 3.5,
            bgcolor: 'background.paper',
            boxShadow: 'var(--fr-e1)'
          }}
        >
          <TextField
            label="用户名"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            required
            autoComplete="username"
            autoFocus
            fullWidth
            size="small"
            slotProps={{ htmlInput: { 'aria-label': '用户名' } }}
          />
          <TextField
            label="口令"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            autoComplete="current-password"
            fullWidth
            size="small"
            slotProps={{ htmlInput: { 'aria-label': '口令' } }}
          />

          {error && (
            <Alert severity="error" role="alert">
              {error}
              {lockSeconds !== null && `（约 ${lockSeconds} 秒后可再试）`}
            </Alert>
          )}

          <Button
            type="submit"
            variant="contained"
            size="large"
            disabled={submitting}
            startIcon={<LoginIcon />}
            sx={{ mt: 0.5 }}
          >
            {submitting ? '正在登录…' : '登录'}
          </Button>

          <Stack direction="row" spacing={1} sx={{ justifyContent: 'center', pt: 0.5 }}>
            <Typography variant="caption" color="text.secondary">
              没有账户？
            </Typography>
            <Typography
              variant="caption"
              component="a"
              href="#/register"
              sx={{ color: 'primary.main', fontWeight: 600, textDecoration: 'none' }}
            >
              注册
            </Typography>
          </Stack>
        </Box>
      </Box>
    </Box>
  )
}

// 视觉验收脚本 v2：登录 → 强制改密 → 逐页截图（浅色 + 深色）。
//
// 为什么必须真跑一遍：这次改造的失败模式几乎全是"编译通过但视觉塌了"——
// 卡片去掉边框后层级消失、深色下某块仍是白底、表单填充色与卡片同色、
// 表格去边后行列糊成一片。只有真渲染才能看到。
//
// 用法（在 web/ 下）：
//   PLAYWRIGHT_BROWSERS_PATH=$PWD/.cache/ms-playwright INIT_PW=<当前口令> \
//     node scripts/shots.mjs
//
// 注意：首启实例的 INIT_PW 是 data/initial-password.txt 里的随机口令；
// 改过一次口令之后要传改后的口令，否则登录步骤会停在登录页。

import { firefox } from 'playwright'
import fs from 'node:fs'
import path from 'node:path'

const BASE = process.env.BASE ?? 'http://127.0.0.1:18121'
// 输出落在仓库根的 tmp/ui/shots2（tmp/ 已忽略，截图不入库）
const OUT = path.resolve('..', 'tmp/ui/shots2')
const USER = 'admin'
const INIT_PW = process.env.INIT_PW ?? ''
const NEW_PW = 'Visual2026!Check'
const only = process.argv[2] // 可选：只跑某个后缀的页面

fs.mkdirSync(OUT, { recursive: true })

const shot = async (page, name) => {
  await page.waitForTimeout(700)
  const p = path.join(OUT, `${name}.png`)
  await page.screenshot({ path: p, fullPage: false })
  console.log('📸', name, fs.statSync(p).size, 'bytes')
}

async function setMode(page, mode) {
  await page.evaluate((m) => localStorage.setItem('fr-color-mode', m), mode)
  await page.reload({ waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(400)
}

const run = async () => {
  const browser = await firefox.launch({ headless: true })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()
  const errors = []
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push(m.text())
  })
  page.on('pageerror', (e) => errors.push(String(e)))

  // ********** 登录页 **********
  await page.goto(`${BASE}/#/login`, { waitUntil: 'domcontentloaded' })
  await setMode(page, 'light')
  await shot(page, '01-login-light')
  await setMode(page, 'dark')
  await shot(page, '02-login-dark')

  // ********** 登录（首启会跳强制改密） **********
  await setMode(page, 'light')
  await page.goto(`${BASE}/#/login`, { waitUntil: 'domcontentloaded' })
  await page.fill('input[autocomplete="username"]', USER)
  await page.fill('input[autocomplete="current-password"]', INIT_PW)
  await page.click('button[type="submit"]')
  await page.waitForTimeout(2000)
  await shot(page, '03-after-login')

  // ********** 强制改密 **********
  const pwInputs = await page.$$('input[type="password"]')
  if (pwInputs.length >= 2) {
    await pwInputs[0].fill(INIT_PW)
    await pwInputs[1].fill(NEW_PW)
    if (pwInputs.length >= 3) await pwInputs[2].fill(NEW_PW)
    const nameInput = await page.$('input[autocomplete="nickname"]')
    if (nameInput) await nameInput.fill('运维管理员')
    await shot(page, '04-password-page')
    await page.click('form button[type="submit"]')
    await page.waitForTimeout(2500)

    await page.goto(`${BASE}/#/login`, { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(500)
    await page.fill('input[autocomplete="username"]', USER)
    await page.fill('input[autocomplete="current-password"]', NEW_PW)
    await page.click('button[type="submit"]')
    await page.waitForTimeout(2200)
    await shot(page, '05-relogin-home')
  }

  // ********** 主要页面（浅色） **********
  const pages = [
    ['/', '10-home'],
    ['/usage/keys', '11-keys'],
    ['/admin/accounts', '12-accounts'],
    ['/admin/groups', '13-groups'],
    ['/admin/inbox', '14-inbox'],
    ['/admin/abstract-models', '15-abstract-models'],
    ['/admin/settings', '16-settings'],
    ['/account', '17-account'],
    ['/contribute', '18-contribute'],
    // 详情页与表单页：这两类页面结构最复杂（KeyValues + Field + DataTable 混排），
    // 也是「卡片去边框后层级丢没丢」最容易暴露的地方。
    ['/admin/accounts/acc_1', '19-account-detail'],
    ['/usage/keys/new', '20-key-new'],
    ['/error/404', '21-error']
  ]
  const list = only ? pages.filter(([, n]) => n.includes(only)) : pages
  for (const [route, name] of list) {
    await page.goto(`${BASE}/#${route}`, { waitUntil: 'domcontentloaded' })
    await shot(page, `${name}-light`)
  }

  // ********** 深色 **********
  await setMode(page, 'dark')
  for (const [route, name] of list) {
    await page.goto(`${BASE}/#${route}`, { waitUntil: 'domcontentloaded' })
    await shot(page, `${name}-dark`)
  }

  // ********** 移动端 **********
  const mobile = await ctx.newPage()
  await mobile.setViewportSize({ width: 400, height: 860 })
  await mobile.goto(`${BASE}/#/`, { waitUntil: 'domcontentloaded' })
  await mobile.evaluate(() => localStorage.setItem('fr-color-mode', 'light'))
  await mobile.reload({ waitUntil: 'domcontentloaded' })
  await mobile.waitForTimeout(800)
  await mobile.screenshot({ path: path.join(OUT, '20-mobile-light.png') })
  const menu = await mobile.$('button[aria-label="打开导航"]')
  if (menu) {
    await menu.click()
    await mobile.waitForTimeout(700)
    await mobile.screenshot({ path: path.join(OUT, '21-mobile-drawer.png') })
  }

  console.log('\n控制台错误:', errors.length ? errors.slice(0, 8) : '无')
  await browser.close()
}

run().catch((e) => {
  console.error('脚本失败:', e.message)
  process.exit(1)
})

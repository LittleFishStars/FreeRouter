const { firefox } = require('/home/ylxc/Projects/app/FreeRouter/web/node_modules/.pnpm/playwright@1.64.1/node_modules/playwright/index.js');
(async () => {
  const browser = await firefox.launch({ headless: true });
  const page = await (await browser.newContext()).newPage();
  const events = [];
  page.on('console', (m) => events.push('CON[' + m.type() + ']: ' + m.text().slice(0, 200)));
  await page.goto('http://127.0.0.1:18700/#/contribute/oauth/raccoon?needs=phone', { timeout: 30000 });
  await page.waitForTimeout(4000);
  const st = await page.evaluate(() => ({
    btn: !!document.getElementById('send-code-btn'),
    cap: !!document.getElementById('captcha-element'),
    sdk: typeof window.initAliyunCaptcha,
    alicdnScripts: [...document.querySelectorAll('script')].filter(s => s.src.includes('alicdn')).length,
  }));
  console.log('状态:', JSON.stringify(st));
  console.log('--- console 事件（含 [captcha] 打点）---');
  console.log(events.filter(e => e.includes('[captcha]') || e.includes('CSP') || e.includes('REFUSED')).slice(0, 8).join('\n') || '(无)');
  await browser.close();
  process.exit(0);
})().catch(e => { console.error('ERR:', e.message.slice(0, 140)); process.exit(1); });

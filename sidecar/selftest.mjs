#!/usr/bin/env node
// sidecar/selftest.mjs —— sidecar 的可独立运行自测（不需要 Go、不需要真 hub 凭据）。
//
// 运行：node sidecar/selftest.mjs
// 退出码：0 全通过 / 1 有失败。hub 不在位时相关用例 Skip（本机必须能被 CI 的裸容器跑过）。
//
// 覆盖：
//   1. 凭据 YAML 的只读解析（三种引号形态 + 注释 + 伪 `refs:` 之外的段）；
//   2. resolve/describe 的语义（不存在 → null；describe 不含值）；
//   3. **只读不写**：调用前后文件内容与 mtime 不变；
//   4. 敏感遮蔽（Bearer / query code / JSON token / sk- 前缀 / 截断 / 幂等）；
//   5. hub 装载（hub 在时）：adapter 能力表与「类名漂移」检测；
//   6. 无可用 adapter 时（hub 在时）：infer.stream / listModels 给结构化
//      E_NOT_IMPLEMENTED 而不是发一个注定失败的请求。
//
// 为什么不放进 Go 的 TestContract：这些断言测的是 Node 进程内部的实现细节
// （YAML 标量、钩子、SSE 拼装），从 Go 侧只能间接观察。Go 侧的契约测试负责
// 「IPC 帧形状与错误码」，这里负责「帧里的内容对不对」。

import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import * as creds from './credentials.mjs';
import { redactLine } from './redact.mjs';

const results = [];
async function test(name, fn) {
  try {
    await fn();
    results.push({ name, ok: true });
    console.log(`  ok   ${name}`);
  } catch (e) {
    if (e && e.skip) {
      results.push({ name, ok: true, skipped: true });
      console.log(`  skip ${name}（${e.message}）`);
      return;
    }
    results.push({ name, ok: false, err: e });
    console.log(`  FAIL ${name}: ${String(e.message || e).split('\n')[0]}`);
  }
}

// —— 1..3 凭据只读解析 ——
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'fr-sidecar-selftest-'));
const credFile = path.join(tmp, '.credentials.yaml');
// 故意混用三种标量形态 + 值里带冒号与花括号 + 一段不属于 refs 的 records
const yaml = [
  'refs:',
  '  PLAIN_KEY: plain-value-abc',
  '  QUOTED_KEY: "a:b{\\"email\\":\\"x@y\\",\\"n\\":1}"',
  "  SINGLE_KEY: 'it''s a value'",
  '  EMPTY_KEY: ""',
  'records:',
  '  some/ns:',
  '    kind: deepseek-account-platform/default',
  '    payload:',
  '      token: SHOULD_NOT_BE_READ',
  'version: 1',
  '',
].join('\n');
fs.writeFileSync(credFile, yaml, { mode: 0o600 });
process.env.FR_DSH_CREDENTIALS_FILE = credFile;

await test('parseRefs 只解析 refs 段（records 里的 token 不外泄）', () => {
  const refs = creds.parseRefs(yaml);
  assert.equal(refs.size, 4);
  assert.equal(refs.has('PLAIN_KEY'), true);
  assert.equal([...refs.values()].some((v) => String(v).includes('SHOULD_NOT_BE_READ')), false);
});

await test('refs 标量：引用形态（JSON 双引号 / 单引号转义）', () => {
  const refs = creds.parseRefs(yaml);
  assert.equal(refs.get('PLAIN_KEY'), 'plain-value-abc');
  assert.equal(refs.get('QUOTED_KEY'), 'a:b{"email":"x@y","n":1}');
  assert.equal(refs.get('SINGLE_KEY'), "it's a value");
  assert.equal(refs.get('EMPTY_KEY'), '');
});

await test('resolve：命中返回原值，空值与不存在都返回 null', () => {
  assert.equal(creds.resolve('PLAIN_KEY'), 'plain-value-abc');
  assert.equal(creds.resolve('EMPTY_KEY'), null); // DSH 口径：空值视为不存在
  assert.equal(creds.resolve('NOPE'), null);
  assert.equal(creds.resolve(''), null);
});

await test('describe：给元数据不给值', () => {
  const d = creds.describe('PLAIN_KEY');
  assert.ok(d);
  const text = JSON.stringify(d);
  assert.equal(text.includes('plain-value-abc'), false, 'describe 不得回显值');
  assert.equal(d.refreshable, false);
  assert.equal(creds.describe('NOPE'), null);
});

await test('只读：调用前后文件内容与 mtime 不变', () => {
  const before = fs.statSync(credFile);
  const beforeText = fs.readFileSync(credFile, 'utf8');
  creds.resolve('PLAIN_KEY');
  creds.describe('QUOTED_KEY');
  creds.hasProvider('plain');
  const after = fs.statSync(credFile);
  assert.equal(fs.readFileSync(credFile, 'utf8'), beforeText);
  assert.equal(after.mtimeMs, before.mtimeMs);
});

await test('hasProvider：按前缀段匹配（不含值）', () => {
  assert.equal(creds.hasProvider('plain'), true);
  assert.equal(creds.hasProvider('zzz'), false);
});

// —— 4 遮蔽 ——
// 两个方向都要钉：真敏感值必须遮；排障字段（错误码/状态名/请求 id）必须留。
// 与 Go 侧 TestContractRedactLineTable 是同一组语义（跨语言副本），改一边必须同步另一边。
await test('redactLine：敏感值（Bearer/Basic/code/JSON token/裸 key/sk-）必须遮', () => {
  const cases = [
    ['Authorization: Bearer sk-abcdefgh12345678', 'abcdefgh12345678'],
    ['Authorization: Basic dXNlcjpwYXNzd29yZA==', 'dXNlcjpwYXNzd29yZA=='],
    ['GET /cb?code=abcd1234efgh&state=ok', 'abcd1234efgh'],
    ['GET /v1/models?key=abcd1234efgh', 'abcd1234efgh'],
    ['{"access_token":"eyJhbGciOiJIUzI1NiJ9"}', 'eyJhbGciOiJIUzI1NiJ9'],
    ['password=hunter2hunter2', 'hunter2hunter2'],
    ['key=sk-1234567890abcdef', '1234567890abcdef'],
  ];
  for (const [input, secret] of cases) {
    const out = redactLine(input);
    assert.equal(out.includes(secret), false, `未遮蔽：${input} → ${out}`);
    assert.ok(out.includes('***'), `应留下遮蔽标记：${out}`);
  }
});

await test('redactLine：排障字段（error_code / status_code / E_ 错误码）不得被遮', () => {
  const cases = [
    ['{"error_code":"upstream_unavailable","message":"上游 502"}', 'upstream_unavailable'],
    ['error_code=model_not_found request_id=abc', 'model_not_found'],
    ['upstream_error code=no_candidate', 'no_candidate'],
    ['code=E_DEPENDENCY_MISSING method=infer.stream', 'E_DEPENDENCY_MISSING'],
    ['status_code=503 upstream=http', '503'],
  ];
  for (const [input, keep] of cases) {
    const out = redactLine(input);
    assert.ok(out.includes(keep), `排障字段被误遮：${input} → ${out}（应保留 ${keep}）`);
  }
});

await test('redactLine：JSON 遮后仍可解析（引号配对保留）', () => {
  const out = redactLine('{"access_token":"ya29.A0AfH6SMBx","refresh_token":"1//0gXyzAbc"}');
  const parsed = JSON.parse(out); // 解析失败会直接抛出
  assert.equal(parsed.access_token, '***');
  assert.equal(parsed.refresh_token, '***');
});

await test('redactLine：幂等 + 截断（截断后仍不含尾部的秘密）', () => {
  const once = redactLine('Authorization: Bearer sk-abcdefgh12345678');
  assert.equal(redactLine(once), once);
  // 分隔符是必须的：真实日志里 key 不会直接粘在字母后面（`token:sk-...`、`"key":"sk-..."`）。
  const long = 'x'.repeat(100) + ' ' + 'sk-abcdefgh12345678' + 'y'.repeat(4000);
  const out = redactLine(long, 256);
  assert.ok(out.length <= 300, '长度应被截断');
  assert.equal(out.includes('sk-abcdefgh12345678'), false);
});

// —— 5..6 hub（不在位则 Skip）——
const hub = await import('./hub.mjs');
let hubOK = false;
await test('hub 装载：能力表与类名漂移检测', async () => {
  const st = await hub.probe();
  if (!st.hubOk) throw Object.assign(new Error('hub 不在本机（跳过 hub 用例）'), { skip: true });
  hubOK = true;
  assert.ok(st.depsOk, 'B 层依赖应可解析');
  assert.ok(st.adapters.length >= 14, 'adapter 能力表应覆盖 14 家以上（全部 OAuth 型）');
  // 不应再有 api_key 型条目：可独立驱动的那种 adapter 已随 modelscope / opencode 下线，
  // 「粘贴 Key」形态改由主进程的内置目录 + 直连出站承担。
  assert.equal(st.adapters.some((a) => a.shape === 'api_key'), false);
  // 类名漂移必须被如实报出，而不是静默 ok
  for (const a of st.adapters) {
    if (!a.ok) assert.ok(a.detail, `${a.provider} 失败时应带 detail`);
  }
});

await test('OAuth 型 provider 如实拒绝（不伪装成可用）', async () => {
  if (!hubOK) throw Object.assign(new Error('hub 不在本机'), { skip: true });
  await assert.rejects(() => hub.adapterFor('trae', 'x'), (e) => e.code === 'E_NOT_IMPLEMENTED');
  // 不在表里 = 没有 adapter：返回 null 而不是抛错，由上层决定怎么报（不在这层替它编原因）。
  assert.equal(await hub.adapterFor('no-such-provider', 'x'), null);
});

await test('infer.stream / listModels：无可用 adapter 时如实拒绝', async () => {
  if (!hubOK) throw Object.assign(new Error('hub 不在本机'), { skip: true });
  // 当前 sidecar 没有任何 apiKey 型 adapter（仅有的两家已下线），OAuth 型又要 DSH
  // 账号池。两个入口都必须给结构化 E_NOT_IMPLEMENTED——这才是「缺能力不崩」的活体
  // 断言：既不发一个注定 401 的请求（把配置问题伪装成上游故障），也不假装成功。
  await assert.rejects(
    () => hub.streamInfer({ provider: 'trae', model: 'm', body: { messages: [{ role: 'user', content: 'hi' }] } }),
    (e) => e.code === 'E_NOT_IMPLEMENTED');
  await assert.rejects(
    () => hub.listModels({ provider: 'trae', apiKey: 'sk-selftest-fake-key' }),
    (e) => e.code === 'E_NOT_IMPLEMENTED');
});

fs.rmSync(tmp, { recursive: true, force: true });

const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} 通过` +
  `（skip ${results.filter((r) => r.skipped).length}）`);
process.exit(failed.length === 0 ? 0 : 1);

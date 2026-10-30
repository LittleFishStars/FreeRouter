#!/usr/bin/env node
// 契约测试用的**假 sidecar**。
//
// 存在的理由：契约测试要覆盖「乱序应答 / stdout 被污染 / 进程中途死掉 / 超时」这些
// 真实 sidecar 几乎不可能按需复现的故障。用真 sidecar 测这些，等于要求它先坏一次。
//
// 它只做三件事：读一行 JSON、按 FAKE_MODE 决定怎么回、把收到的请求打到 stderr。
// 全程不接触任何真实凭据，也不 import providers-hub。
//
// 环境变量：
//   FAKE_MODE      见下方 MODES
//   FAKE_DELAY_MS  每个应答延迟多少毫秒（用来暴露并发——延迟期间 busy 为真）
//
// 打到 stderr 的标记（Go 侧测试断言它们）：
//   REQ <id> <method>   收到一个请求（id 单调递增由测试从这里验）
//   CONCURRENT <id> ... 上一个请求还没回完就来了下一个 → 说明 Go 侧没串行化
//   BAD_FRAME ...       收到不是 JSON 的行

import process from 'node:process';

const mode = process.env.FAKE_MODE || 'ok';
const delayMs = Number(process.env.FAKE_DELAY_MS || '0');

let busy = false;
let buf = '';

const write = (obj) => process.stdout.write(JSON.stringify(obj) + '\n');
const logf = (s) => process.stderr.write(s + '\n');

const okFrame = (req, result) => ({ id: req.id, ok: true, result });
const errFrame = (req, code, message, retryable = false) => ({
  id: req.id,
  ok: false,
  error: { code, message, retryable },
});

// 所有 provider 都用一个假名字，避免测试里出现真实厂商名而让人误以为连了真上游。
const FAKE_PROVIDER = 'fake_provider';

function handleLine(line) {
  let req;
  try {
    req = JSON.parse(line);
  } catch {
    logf('BAD_FRAME ' + line);
    return;
  }
  if (busy) {
    // 这条本身不改变行为，只是留证：Go 侧契约要求一问一答。
    logf(`CONCURRENT ${req.id} ${req.method}`);
  }
  busy = true;
  logf(`REQ ${req.id} ${req.method}`);

  const reply = (payload, then) => {
    const emit = () => {
      write(payload);
      busy = false;
      if (then) then();
    };
    if (delayMs > 0) setTimeout(emit, delayMs);
    else emit();
  };

  // —— health 唯一特殊：它是启动握手，多数故障模式都要让它保持正常，
  //    否则 Start 就直接失败了，测试根本走不到运行期那一步。
  if (req.method === 'health') {
    if (mode === 'health_fail') {
      return reply(errFrame(req, 'E_DEPENDENCY_MISSING', '缺 @deepseek-ai/dsh-credentials'));
    }
    if (mode === 'health_exit') {
      process.exit(4);
    }
    return reply(
      okFrame(req, {
        version: '0.0.0-fake',
        providers: [FAKE_PROVIDER],
        deps: [{ name: '@deepseek-ai/dsh-credentials', ok: true }],
        stateReadonly: true,
        nodeVersion: process.version,
      }),
    );
  }

  if (mode === 'silent') return; // 不回、不退 —— 让 Go 侧超时
  if (mode === 'exit_on_call') process.exit(3);
  if (mode === 'unknown_code') {
    return reply(errFrame(req, 'E_SOMETHING_BRAND_NEW', '未来版本新增的错误码', true));
  }

  const noisy = mode === 'noise';
  const payload = (frame) => (noisy ? frame : frame);

  const send = (frame, then) => {
    if (mode === 'noise') {
      // 第三方库往 stdout 写日志的真实形态：一行非 JSON。
      process.stdout.write('[fake] something logged to stdout\n');
    }
    if (mode === 'wrong_id') {
      // 先回一个对不上 id 的帧，再回正确的 —— 模拟「上一帧无人认领」。
      write({ id: req.id + 100, ok: true, result: { stray: true } });
    }
    reply(payload(frame), then);
  };

  switch (req.method) {
    case 'providers.list':
      return send(
        okFrame(req, [
          {
            provider: FAKE_PROVIDER,
            displayName: '假供应商',
            loginKind: 'device_code',
            loginImplemented: true,
            hasAccount: true,
          },
        ]),
      );

    case 'models.list':
      return send(
        okFrame(req, [
          {
            upstreamModelId: `${FAKE_PROVIDER}/fake-flash`,
            displayName: '假 Flash',
            contextWindow: null,
            modalities: ['text'],
            efforts: ['low', 'high'],
          },
        ]),
      );

    case 'credentials.resolve': {
      const ref = req.params?.credentialRef ?? '';
      if (ref.includes('missing')) return send(okFrame(req, null));
      return send(okFrame(req, { value: 'fake-secret-value', source: 'keychain' }));
    }

    case 'credentials.describe':
      return send(
        okFrame(req, {
          provider: FAKE_PROVIDER,
          externalLabel: 'fake@example.invalid',
          expiresAt: '2030-01-01T00:00:00Z',
          refreshable: true,
        }),
      );

    // infer.stream：返回 M8 之后 sidecar 的真实形状——SSE 文本装进 {"sse":...} 信封。
    // 契约测试用它验证 relay 必须先拆信封再交给 adapter 的 StreamReader；不拆会得到
    // 「200 + 空回答」（信封不是 SSE，reader 只能看到 EOF）——比 502 更难发现。
    case 'infer.stream':
      return send(
        okFrame(req, {
          sse:
            'data: {"id":"chatcmpl-fake","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}\n\n' +
            'data: {"id":"chatcmpl-fake","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}\n\n' +
            'data: [DONE]\n\n',
        }),
      );

    case 'login.start':
      return send(
        okFrame(req, {
          sessionId: 'sess-1',
          kind: 'device_code',
          instructions: '请访问示例地址并输入用户码',
          verificationUrl: 'https://example.invalid/device',
          userCode: 'ABCD-1234',
          qrData: null,
          callbackPort: null,
          expiresAt: '2030-01-01T00:05:00Z',
        }),
      );

    case 'login.poll':
      return send(
        okFrame(req, {
          status: 'ok',
          credentialRef: 'FAKE_ACCOUNT_012345',
          secretRef: null,
          detail: null,
        }),
      );

    case 'login.submit':
      return send(okFrame(req, { status: 'pending' }));

    case 'login.cancel':
      return send(okFrame(req, {}));

    case 'credits.query':
      return send(
        okFrame(req, {
          balance: 1.5,
          quotaUsed: 10,
          quotaTotal: 100,
          unit: 'usd',
          raw: { upstream: 'fake' },
        }),
      );

    case 'credits.claim':
      // 签到桩：恒回「今天已签到」——契约只验形状（outcome 信封 + 轮换字段
      // 可选），不验渠道语义；unsupported/failed 分支由 app 层单测覆盖。
      return send(
        okFrame(req, {
          ok: true,
          outcome: { kind: 'already-claimed', message: '今天已签到' },
        }),
      );

    case 'shutdown':
      // 回包后让事件循环自然排空再退出（直接 process.exit 会截断 stdout）。
      return send(okFrame(req, {}), () => setImmediate(() => process.exit(0)));

    // 红线守卫的**反面**证据：假 sidecar 明确拒绝写凭据方法。
    // 真实 sidecar 与 Go 客户端都不提供这些方法，这里再挡一道。
    case 'credentials.set':
    case 'credentials.unset':
    case 'credentials.refresh':
      return send(errFrame(req, 'E_CREDENTIAL_READONLY', '凭据只读：续期只能由 DSH 完成'));

    default:
      return send(errFrame(req, 'E_UNKNOWN_METHOD', `未知方法 ${req.method}`));
  }
}

process.stdin.setEncoding('utf8');
process.stdin.on('data', (chunk) => {
  buf += chunk;
  let idx;
  while ((idx = buf.indexOf('\n')) >= 0) {
    const line = buf.slice(0, idx);
    buf = buf.slice(idx + 1);
    if (line.trim().length > 0) handleLine(line);
  }
});
process.stdin.on('end', () => process.exit(0));

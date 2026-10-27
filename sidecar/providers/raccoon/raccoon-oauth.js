/**
 * Raccoon Work 登录与续期的纯 API 层。
 *
 * ## 两条登录路径
 *
 * 1. **微信扫码**（首选）：`code` 由**客户端本地随机生成**，服务端只做轮询查询。
 *    实测任意自造 code 都被接受并进入 `pending`，故完全绕开官方那条
 *    `office-raccoon://auth/callback` 自定义协议回调 —— 那是本插件（宿主侧
 *    Node 进程）无法接收的。
 * 2. **短信验证码**（次选）：手机号需 AES-128-CFB 加密，且 `send_sms` 要求
 *    先过阿里云滑块拿到 `captcha_param`。
 *
 * ## 为什么不用官方桌面端的授权码链路
 *
 * `desktopLogin.js` 走「网页授权 → `office-raccoon://auth/callback?code=` →
 * `POST /login_with_authorization_code`」。回调地址在网页里是**写死的**，
 * 改不成 localhost，插件收不到回调。故该链路只保留 `exchangeRaccoonAuthorizationCode`
 * 供将来（如支持自定义协议的环境）复用。
 *
 * ## 本模块是纯 API 层
 *
 * 不含本地 HTTP 服务器（那是 `raccoon-login-page.ts` 的职责），
 * 也不做凭据持久化（那是 `raccoon-auth.ts` 的职责）。全部函数可离线单测。
 */
import { randomBytes } from 'node:crypto';
import { RACCOON_QR_STATUS, RACCOON_REQUEST_TIMEOUT_MS, decodeJwtExpMs, encryptRaccoonPhone, raccoonHeaders, } from './raccoon.js';
/** 解析业务信封。`code === 0` 为成功。 */
function parseEnvelope(payload, status) {
    const record = typeof payload === 'object' && payload !== null && !Array.isArray(payload)
        ? payload
        : {};
    const code = typeof record.code === 'number' ? record.code : (status >= 400 ? status : 0);
    const message = typeof record.message === 'string' ? record.message : '';
    const details = typeof record.details === 'string' ? record.details : '';
    const data = typeof record.data === 'object' && record.data !== null && !Array.isArray(record.data)
        ? record.data
        : undefined;
    return { code, message, details, data };
}
/** 把信封里的错误拼成一条可读信息（服务端文案优先）。 */
function envelopeError(envelope, fallback) {
    const parts = [envelope.message, envelope.details].filter((s) => s.length > 0);
    const text = parts.length > 0 ? parts.join(': ') : fallback;
    return new Error(`raccoon: ${text}`);
}
/** 发一次 JSON 请求并解析信封。method 默认 POST（取消会话用 PUT）。 */
async function postJson(url, body, fetcher, headers = {}, method = 'POST') {
    let response;
    try {
        response = await fetcher(url, {
            method,
            headers: { 'Content-Type': 'application/json', ...headers },
            body: JSON.stringify(body),
            signal: AbortSignal.timeout(RACCOON_REQUEST_TIMEOUT_MS),
        });
    }
    catch (error) {
        throw new Error(`raccoon: 请求失败：${error instanceof Error ? error.message : String(error)}`);
    }
    let parsed;
    try {
        parsed = await response.json();
    }
    catch {
        throw new Error(`raccoon: 响应不是 JSON（HTTP ${response.status}）`);
    }
    return { envelope: parseEnvelope(parsed, response.status), status: response.status };
}
// ── 现行扫码协议（2026-10-27 从官网登录页逆向，x-raccoon-website chunks）──
//
// 旧协议（客户端本地造 code + `login_with_qrcode_code` 轮询）已失效：
// ① 二维码指向的 `login/mp` 网页 404（上游下线了微信内路径）；② App 扫码
// 报「二维码内容无效」——现行协议是**服务端主导**的会话：
//   POST /auth/mp/qrcode        → { qrcode_code, scan_path, expires_in, poll_interval }
//   二维码内容                   = qrOrigin + scan_path?code=…&appname=…&source=platform
//   POST /auth/mp/qrcode/login  → { status: pending|logging|canceled|success, access_token? }
//   PUT  /auth/mp/qrcode        → { qrcode_code, status: "canceled" }（取消）

/** 二维码的 origin（官网登录页服务端注入的 qrOrigin，配置化以便上游迁移）。 */
export const RACCOON_QR_ORIGIN = 'https://xiaohuanxiong.com/';
/** 现行扫码协议的 API 前缀（官网前端 baseUrl 默认 /api/iam/v1，同源）。 */
export const RACCOON_IAM_BASE = 'https://xiaohuanxiong.com/api/iam/v1';

/**
 * 创建一次扫码会话。
 *
 * @returns { qrcode_code, scan_path, expires_in(秒), poll_interval(秒) }
 * productCodes 可空——官网允许不带（对应 token 的产品授权范围）。
 */
export async function createQrSession(product, productCodes, fetcher = fetch) {
    const body = Array.isArray(productCodes) && productCodes.length > 0
        ? { product_codes: productCodes }
        : {};
    const { envelope } = await postJson(`${RACCOON_IAM_BASE}/auth/mp/qrcode`, body, fetcher);
    if (envelope.code !== 0 || typeof envelope.data !== 'object' || envelope.data === null) {
        throw new Error(`raccoon: 创建扫码会话失败（${envelope.message || envelope.code || '未知错误'}）`);
    }
    const d = envelope.data;
    // 形状校验对齐官网（scan_path 必须是站内相对路径）。
    if (typeof d.qrcode_code !== 'string' || !d.qrcode_code
        || typeof d.scan_path !== 'string' || !d.scan_path.startsWith('/')
        || d.scan_path.startsWith('//') || /[\\\s]/.test(d.scan_path)
        || !Number.isFinite(d.expires_in) || d.expires_in <= 0) {
        throw new Error('raccoon: 扫码会话应答形状非法');
    }
    return {
        qrcodeCode: d.qrcode_code,
        scanPath: d.scan_path,
        expiresInSec: d.expires_in,
        pollIntervalSec: Number.isFinite(d.poll_interval) && d.poll_interval > 0 ? d.poll_interval : 2,
    };
}

/**
 * 构造二维码承载的 URL（对齐官网 buildQr 的严格校验）：
 * qrOrigin + scan_path，search 清空后设 code/appname/source=platform。
 *
 * 为什么校验这么严：scan_path 来自服务端应答，官网拒绝一切「换源/带凭据/
 * 开放重定向」形态（协议式校验防钓鱼）——照抄，行为一致才扫得出来。
 */
export function buildQrContent(qrOrigin, scanPath, code, brand) {
    const base = new URL(qrOrigin);
    if (base.protocol !== 'https:' || base.username || base.password
        || !scanPath.startsWith('/') || scanPath.startsWith('//')
        || /[\\\s#]/.test(scanPath)) {
        throw new Error('raccoon: QR origin/scan_path 形态非法');
    }
    const decoded = decodeURIComponent(scanPath);
    if (/[\\\s#]/.test(decoded) || decoded.startsWith('//') || /%[0-9a-f]{2}/i.test(decoded)) {
        throw new Error('raccoon: QR scan_path 含非法编码');
    }
    const u = new URL(scanPath, base);
    if (u.origin !== base.origin || u.hash) {
        throw new Error('raccoon: QR scan_path 跨源或带 hash');
    }
    u.search = '';
    u.searchParams.set('code', code);
    u.searchParams.set('appname', brand);
    u.searchParams.set('source', 'platform');
    return u.href;
}

/** 轮询一次扫码会话状态。status: pending | logging | canceled | success。 */
export async function pollQrSession(product, qrcodeCode, fetcher = fetch) {
    const { envelope } = await postJson(
        `${RACCOON_IAM_BASE}/auth/mp/qrcode/login`,
        { qrcode_code: qrcodeCode }, fetcher);
    if (envelope.code !== 0 || typeof envelope.data !== 'object' || envelope.data === null) {
        throw new Error(`raccoon: 查询扫码状态失败（${envelope.message || envelope.code || '未知错误'}）`);
    }
    const d = envelope.data;
    const status = typeof d.status === 'string' ? d.status : '';
    if (!['pending', 'logging', 'canceled', 'success'].includes(status)) {
        throw new Error(`raccoon: 扫码状态未知（${status || '空'}）`);
    }
    const accessToken = typeof d.access_token === 'string' ? d.access_token : '';
    if (status === 'success' && !accessToken) {
        throw new Error('raccoon: 会话 success 但未返回 token');
    }
    const refreshToken = typeof d.refresh_token === 'string' ? d.refresh_token : '';
    const expMs = accessToken ? decodeJwtExpMs(accessToken) : undefined;
    return {
        status,
        accessToken,
        refreshToken,
        ...expMs !== undefined ? { expiresAt: String(expMs) } : {},
    };
}

/** 取消一次扫码会话（用户点「放弃」时调用，服务端把会话置 canceled）。 */
export async function cancelQrSession(product, qrcodeCode, fetcher = fetch) {
    try {
        await postJson(`${RACCOON_IAM_BASE}/auth/mp/qrcode`,
            { qrcode_code: qrcodeCode, status: 'canceled' }, fetcher, {}, 'PUT');
    } catch {
        // 取消失败无所谓：会话自己会过期。
    }
}

/**
 * （已废弃）生成一个扫码用的 `qrcode_code`——旧协议客户端本地造 code，
 * 现行协议由服务端在 createQrSession 里下发。保留导出仅为兼容引用检查。
 */
export function generateQrCode() {
    return randomBytes(16).toString('hex');
}
/**
 * 构造二维码承载的微信登录页 URL。
 *
 * 依据客户端：`` `${base}/login/mp?code=${code}&appname=商汤小浣熊官网` ``。
 * 这是一个**公开页面**，扫码后在微信内完成授权，服务端据此把该 code 置为
 * `success`，我们轮询 `login_with_qrcode_code` 取回 token。
 */
export function buildQrImageUrl(product, code) {
    const params = new URLSearchParams({ code, appname: '商汤小浣熊官网' });
    return `${product.apiBase}/login/mp?${params.toString()}`;
}
/**
 * 轮询一次扫码登录状态。
 *
 * ⚠️ **任何异常都降级为 `pending`**（网络抖动、响应畸形、未知 status）：
 * 轮询是 2 秒一次的循环，偶发失败不应中断整个登录流程；而把未知状态
 * 误判成 `success` 会让流程拿到空 token 后卡死，误判成 `canceled` 则会让
 * 用户正在扫码的二维码被无故刷新。
 */
export async function pollRaccoonQrLogin(product, code, fetcher = fetch) {
    let envelope;
    try {
        const result = await postJson(`${product.apiBase}${product.authApiPrefix}/login_with_qrcode_code`, { qrcode_code: code }, fetcher);
        envelope = result.envelope;
    }
    catch {
        return { status: 'pending' };
    }
    // 业务错误**不再吞成 pending**（2026-10-27 用户实测「手机提示二维码已过期
    // 或已被使用，电脑一直等待扫码」）：服务端对过期/已用 code 会返回非 0
    // envelope + 原因文本，吞掉它电脑就永远「等待扫码」，用户两端都看不到
    // 真实原因。透传为 failed（message 原文上抛）。
    if (envelope.code !== 0) {
        return {
            status: 'failed',
            message: envelope.message || `raccoon: 业务错误 code=${envelope.code}`,
        };
    }
    if (envelope.data === undefined)
        return { status: 'pending' };
    const raw = envelope.data.status;
    const status = typeof raw === 'string' ? raw : '';
    const expiredAt = typeof envelope.data.expired_at === 'string' ? envelope.data.expired_at : undefined;
    // 会话过期**主动判定**（2026-10-27 用户实测：手机提示二维码已过期，电脑
    // 永远「等待扫码」）：服务端的 status 枚举里没有 expired（过期后返回的
    // 可能仍是 pending 或未知值），但 expired_at 每帧都在应答里——按它判定，
    // 过期即 failed，前端立刻提示重新发起，而不是两端干等。
    if (expiredAt !== undefined) {
        const expMs = Date.parse(expiredAt);
        if (!Number.isNaN(expMs) && Date.now() > expMs) {
            return { status: 'failed', message: '二维码已过期，请重新发起授权' };
        }
    }
    if (status === RACCOON_QR_STATUS.canceled)
        return { status: 'canceled' };
    if (status === RACCOON_QR_STATUS.logging) {
        return { status: 'logging', ...expiredAt !== undefined ? { expiredAt } : {} };
    }
    if (status === RACCOON_QR_STATUS.success) {
        const accessToken = typeof envelope.data.access_token === 'string' ? envelope.data.access_token : '';
        const refreshToken = typeof envelope.data.refresh_token === 'string' ? envelope.data.refresh_token : '';
        // ⚠️ 缺 token 的 success 视为未完成：否则会产出空凭据并让流程卡死
        if (accessToken.length === 0)
            return { status: 'pending' };
        const expMs = decodeJwtExpMs(accessToken);
        return {
            status: 'success',
            accessToken,
            refreshToken,
            ...expMs !== undefined ? { expiresAt: String(expMs) } : {},
        };
    }
    return { status: 'pending' };
}
// ── 现行短信协议（2026-10-27 从官网登录页逆向，x-raccoon-website chunks）──
//
// 旧协议（/api/web/auth/v1 的 send_sms/login_with_sms + RSA 加密手机号）整组
// 废弃：现行协议在 /api/iam/v1 下，会话键是服务端下发的 verification_id
//（手机号明文直传），滑块产物字段名也改叫 captcha_token。
//
//   发码  POST /api/iam/v1/sms/send
//         { nation_code, phone, captcha_token, scene: "login" }
//       → { verification_id, expires_in, retry_after }
//   登录  POST /api/iam/v1/auth/login/sms
//         { nation_code, phone, verification_id, verification_code }
//       → { access_token, token_type: "bearer" }（无 refresh_token，按 JWT exp 过期）

/**
 * 下发短信验证码。
 *
 * @param captchaToken 阿里云滑块产物（官网字段名 captcha_token；缺省时服务端
 *   拒绝发送）。
 * @returns verificationId —— 登录时必须原样带回的会话键。
 */
export async function sendSmsVerification(product, phone, captchaToken, fetcher = fetch) {
    const { envelope } = await postJson(`${RACCOON_IAM_BASE}/sms/send`, {
        nation_code: '86',
        // phone 与旧协议同样走 AES-128-CFB 加密（官网 SSR 下发的 publicKey
        // 就是 'senseraccoon2023'，与旧密钥同值；明文会被判 phone invalid）
        phone: encryptRaccoonPhone(phone.trim()),
        captcha_token: captchaToken,
        scene: 'login',
    }, fetcher);
    if (envelope.code !== 0 && envelope.code !== 'OK') {
        throw new Error(`raccoon: 下发验证码失败（${envelope.message || envelope.code || '未知错误'}）`);
    }
    const d = envelope.data ?? {};
    if (typeof d.verification_id !== 'string' || !d.verification_id) {
        throw new Error('raccoon: 发码应答缺 verification_id（形状非法）');
    }
    return d.verification_id;
}

/**
 * 短信验证码登录。
 *
 * @param verificationId 发码应答里拿到的会话键（原样带回）。
 * @returns 凭据 { access_token, refresh_token, expires_at? }——现行应答不含
 *   refresh_token（无静默续期），过期时间从 access_token 的 JWT exp 推。
 */
export async function smsLogin(product, phone, verificationId, verificationCode, fetcher = fetch) {
    const { envelope } = await postJson(`${RACCOON_IAM_BASE}/auth/login/sms`, {
        nation_code: '86',
        phone: encryptRaccoonPhone(phone.trim()),
        verification_id: verificationId,
        verification_code: verificationCode,
    }, fetcher);
    if (envelope.code !== 0 && envelope.code !== 'OK') {
        throw new Error(`raccoon: 短信登录失败（${envelope.message || envelope.code || '未知错误'}）`);
    }
    const d = envelope.data ?? {};
    const accessToken = typeof d.access_token === 'string' ? d.access_token : '';
    if (!accessToken) throw new Error('raccoon: 短信登录应答缺 access_token');
    const expMs = decodeJwtExpMs(accessToken);
    return {
        access_token: accessToken,
        refresh_token: '',
        ...expMs !== undefined ? { expires_at: String(expMs) } : {},
    };
}

/** 从成功信封里取出凭据；缺 `access_token` 时抛错（不产出半截凭据）。 */
function credentialFromEnvelope(envelope) {
    const data = envelope.data ?? {};
    const accessToken = typeof data.access_token === 'string' ? data.access_token : '';
    const refreshToken = typeof data.refresh_token === 'string' ? data.refresh_token : '';
    if (accessToken.length === 0) {
        throw new Error('raccoon: 登录响应缺少 access_token');
    }
    const expMs = decodeJwtExpMs(accessToken);
    const officeIdentity = typeof data.office_identity === 'string' ? data.office_identity : '';
    return {
        access_token: accessToken,
        refresh_token: refreshToken,
        ...expMs !== undefined ? { expires_at: String(expMs) } : {},
        ...officeIdentity.length > 0 ? { office_identity: officeIdentity } : {},
    };
}
/**
 * 用授权码换取凭据（官方桌面端链路）。
 *
 * 本插件**当前不走这条路**（收不到自定义协议回调），保留它是为了让
 * 「官方链路」有可单测的落点，并在将来支持自定义协议时可直接复用。
 */
export async function exchangeRaccoonAuthorizationCode(product, authorizationCode, fetcher = fetch) {
    const { envelope } = await postJson(`${product.apiBase}${product.authApiPrefix}/login_with_authorization_code`, { authorization_code: authorizationCode }, fetcher);
    if (envelope.code !== 0) {
        // 官方约定：200035 = 授权码不存在/过期/已消费
        if (envelope.code === 200035) {
            throw new Error('raccoon: 授权码已失效，请重新发起登录');
        }
        throw envelopeError(envelope, '授权码登录失败');
    }
    return credentialFromEnvelope(envelope);
}
/**
 * 用 refresh_token 换新凭据。
 *
 * ⚠️ 服务端可能**只返回新的 access_token**（不带新 refresh_token），
 * 此时必须**保留旧值** —— 否则续期一次就把账号变成不可续期。
 * ⚠️ 服务端不返回的附加字段（昵称、身份、设备号）也要保留。
 * ⚠️ 401 表示 refresh_token 已失效，**抛「请重新登录」且不重试**。
 */
export async function refreshRaccoonCredential(product, credential, fetcher = fetch) {
    const { envelope, status } = await postJson(`${product.apiBase}${product.authApiPrefix}/refresh`, { refresh_token: credential.refresh_token }, fetcher);
    if (status === 401 || envelope.code === 200003) {
        throw new Error('raccoon: 登录态已过期，请重新登录');
    }
    if (envelope.code !== 0)
        throw envelopeError(envelope, '续期失败');
    const data = envelope.data ?? {};
    const accessToken = typeof data.access_token === 'string' ? data.access_token : '';
    if (accessToken.length === 0) {
        throw new Error('raccoon: 续期响应缺少 access_token');
    }
    const nextRefresh = typeof data.refresh_token === 'string' && data.refresh_token.length > 0
        ? data.refresh_token
        : credential.refresh_token;
    const expMs = decodeJwtExpMs(accessToken);
    return {
        ...credential,
        access_token: accessToken,
        refresh_token: nextRefresh,
        ...expMs !== undefined ? { expires_at: String(expMs) } : {},
    };
}
/**
 * 拉取用户信息（展示用）。
 *
 * ⚠️ 失败时返回**空对象**而不是抛错：用户信息只用于昵称展示，
 * 不该因为它失败而让整个登录流程失败（登录已经成功了）。
 *
 * ⚠️ `nickname` 取的是远端的 `name`，而**它是服务端自动生成的默认名**
 *（实测 `RaccoonAva`），微信扫码不回传微信昵称 —— 故多账号消歧要靠 `phone`。
 */
export async function fetchRaccoonUserInfo(product, credential, fetcher = fetch) {
    try {
        const response = await fetcher(`${product.apiBase}${product.authApiPrefix}/user_info`, {
            method: 'GET',
            headers: raccoonHeaders(credential),
            signal: AbortSignal.timeout(RACCOON_REQUEST_TIMEOUT_MS),
        });
        if (!response.ok)
            return {};
        const envelope = parseEnvelope(await response.json(), response.status);
        if (envelope.code !== 0 || envelope.data === undefined)
            return {};
        const userId = typeof envelope.data.id === 'string' ? envelope.data.id : '';
        const nickname = typeof envelope.data.name === 'string' ? envelope.data.name : '';
        const officeIdentity = typeof envelope.data.office_identity === 'string'
            ? envelope.data.office_identity
            : '';
        const phone = typeof envelope.data.phone === 'string' ? envelope.data.phone : '';
        return {
            ...userId.length > 0 ? { userId } : {},
            ...nickname.length > 0 ? { nickname } : {},
            ...officeIdentity.length > 0 ? { officeIdentity } : {},
            ...phone.length > 0 ? { phone } : {},
        };
    }
    catch {
        return {};
    }
}
//# sourceMappingURL=raccoon-oauth.js.map
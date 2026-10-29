// 凭据余额/积分查询（统一入口）。
//
// 来源：dsh-providers-hub 的 *-credits.js 各家实现（逐函数移植，行为对齐）。
// 只做**只读查询**——签到/领取类写操作不在本文件（贡献面不代用户消费）。
//
// 形状契约（queryCreditBalance 的返回）：
//   { ok: true,  total: number, unit: string, packages?: [{name, amount}] }
//   { ok: false, error: string }
// 「查不到」与「失败」分开：上游没给余额字段 → ok:true + total:null（卡片显示
// 「上游未报余额」），网络/鉴权失败 → ok:false + error（可重试）。

import * as raccoonLib from './raccoon/raccoon.js';
import { parseEnvelope } from './raccoon/raccoon-oauth.js';
import { LOOMY_API_BASE } from './loomy/loomy.js';
import { MINIMAX, MINIMAX_CREDIT_DETAILS_PATH } from './minimax/minimax-product.js';
import { minimaxFetch, minimaxHeadersFor } from './minimax/minimax.js';

const REQUEST_TIMEOUT_MS = 30_000;
// minimax 用自己模块的超时常量（MINIMAX_REQUEST_TIMEOUT_MS 未随我们的快照移植）

/** 把数字字段读出来（缺失/非数 → undefined，不编造 0）。 */
function readNumber(v) {
    return typeof v === 'number' && Number.isFinite(v) ? v : undefined;
}

// ── Raccoon：GET /api/web/points/v1/balance（积分，多池分项）──

function raccoonHeaders(credential) {
    const headers = {
        Accept: 'application/json',
        'Content-Type': 'application/json',
        Authorization: `Bearer ${credential.access_token}`,
        // 个人账号为空串；客户端总是发送该头
        'X-Org-Code': credential.office_identity ?? '',
        'X-Raccoon-Language': 'zh',
    };
    if (credential.device_id !== undefined && credential.device_id.length > 0) {
        headers['X-Device-Id'] = credential.device_id;
    }
    return headers;
}

async function queryRaccoon(credential) {
    const product = { apiBase: 'https://xiaohuanxiong.com', clientPlatform: 'desktop-windows' };
    const url = `${product.apiBase}${raccoonLib.RACCOON_POINTS_PREFIX}/balance`;
    let envelope;
    try {
        const response = await fetch(url, {
            method: 'GET',
            headers: raccoonHeaders(credential),
            signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
        });
        envelope = parseEnvelope(await response.json(), response.status);
    } catch (error) {
        return { ok: false, error: `raccoon: 请求失败（${error instanceof Error ? error.message : String(error)}）` };
    }
    if (!envelope.ok || envelope.data === undefined) {
        return { ok: false, error: `raccoon: ${envelope.message || '余额查询被拒'}` };
    }
    const available = readNumber(envelope.data.available_points);
    if (available === undefined) {
        // 上游没给核心字段：如实报「未报余额」，不编造 0
        return { ok: true, total: null, unit: '积分' };
    }
    const packages = [];
    for (const [name, key] of [['奖励积分', 'reward_points'], ['每日积分', 'daily_points'], ['会员积分', 'monthly_points'], ['充值积分', 'topup_points']]) {
        const n = readNumber(envelope.data[key]);
        if (n !== undefined && (name !== '会员积分' || n > 0)) packages.push({ name, amount: n });
    }
    return {
        ok: true,
        total: available,
        unit: '积分',
        packages: packages.length > 0 ? packages : [{ name: '可用积分', amount: available }],
    };
}

// ── Loomy：GET /points/records（balance=永久池、dailyBalance=每日池）──

async function queryLoomy(credential) {
    const headers = { Accept: 'application/json', token: credential.access_token };
    let response;
    try {
        response = await fetch(`${LOOMY_API_BASE}/points/records?pageNo=1&pageSize=1&recordType=all`, {
            method: 'GET', headers, signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
        });
    } catch (error) {
        return { ok: false, error: `loomy: 请求失败（${error instanceof Error ? error.message : String(error)}）` };
    }
    let payload;
    try { payload = await response.json(); } catch {
        return { ok: false, error: `loomy: 响应不是 JSON（HTTP ${response.status}）` };
    }
    if (payload.code !== 0) {
        return { ok: false, error: `loomy: ${payload.message || `错误码 ${payload.code}`}` };
    }
    const read = (v) => (typeof v === 'number' && Number.isFinite(v) ? v : undefined);
    const permanent = read(payload.data?.balance);
    const daily = read(payload.data?.dailyBalance) ?? 0;
    if (permanent === undefined) {
        return { ok: true, total: null, unit: '积分' };
    }
    return {
        ok: true,
        total: permanent + daily,
        unit: '积分',
        packages: [
            { name: '永久积分', amount: permanent },
            { name: '每日赠送', amount: daily },
        ],
    };
}

// ── MiniMax：GET /minimax-cloud/api/v1/credit/details（credit 包列表，求和）──

async function queryMinimax(credential) {
    const url = `${MINIMAX.apiHost}${MINIMAX_CREDIT_DETAILS_PATH}`;
    let response;
    try {
        // minimaxFetch：带传输重试 + 保凭据跟随 307（agent.minimax.io→minimaxi.com），
        // 与推理/目录路径共用同一份实现（hub 同因注释：跨 origin 跟随剥 Authorization）。
        response = await minimaxFetch(url, () => {
            const headers = minimaxHeadersFor(credential, undefined, MINIMAX.id);
            headers.set('Accept', 'application/json');
            return { method: 'GET', headers, signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS) };
        }, { fetcher: fetch });
    } catch (error) {
        return { ok: false, error: `minimax: 请求失败（${error instanceof Error ? error.message : String(error)}）` };
    }
    let payload;
    try { payload = await response.json(); } catch {
        return { ok: false, error: `minimax: 响应不是 JSON（HTTP ${response.status}）` };
    }
    // 余额响应是平铺的（details/total_count 与 base_resp 同级，无 data）
    const details = Array.isArray(payload.details) ? payload.details : undefined;
    if (details === undefined && (typeof payload.total_count !== 'number' || !Number.isFinite(payload.total_count))) {
        return { ok: false, error: 'minimax: 余额响应形状不符合预期（服务端可能改版）' };
    }
    let total = 0;
    const packages = [];
    for (const entry of details ?? []) {
        if (typeof entry !== 'object' || entry === null) continue;
        const remaining = typeof entry.remaining_amount === 'number' && Number.isFinite(entry.remaining_amount)
            ? entry.remaining_amount : undefined;
        if (remaining === undefined) continue;
        total += remaining;
        // 包名用发放日期（credit_type 语义未实测，不编造业务含义）
        const granted = typeof entry.granted_at_ms === 'number' ? new Date(entry.granted_at_ms).toISOString().slice(0, 10) : '未知';
        packages.push({ name: `${granted} 发放`, amount: remaining });
    }
    if (details === undefined && typeof payload.total_count === 'number') {
        total = payload.total_count; // 只有总数没有分项：直接用总数
    }
    return { ok: true, total, unit: 'credit', packages: packages.length > 0 ? packages : undefined };
}

const QUERY_BY_PROVIDER = {
    raccoon: queryRaccoon,
    loomy: queryLoomy,
    minimax: queryMinimax,
};

/** 支持余额查询的 provider 清单（贡献入口/管理面据此显示「查余额」能力）。 */
export function balanceSupportedProviders() {
    return Object.keys(QUERY_BY_PROVIDER);
}

/**
 * 查一个凭据的余额/积分。
 *
 * @param credential 已解密的凭据对象（JSON.parse 后的明文形状）
 */
export async function queryCreditBalance(provider, credential) {
    const fn = QUERY_BY_PROVIDER[provider];
    if (!fn) {
        return { ok: false, error: `${provider} 暂不支持余额查询` };
    }
    if (typeof credential?.access_token !== 'string' || credential.access_token.length === 0) {
        return { ok: false, error: '凭据缺少 access_token，无法查询余额' };
    }
    return fn(credential);
}

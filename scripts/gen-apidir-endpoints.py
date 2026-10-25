#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""gen-apidir-endpoints.py —— 给 internal/apidir/catalog.json 原地补齐 endpoints 对象。

契约来源：tmp/candidate-sources-contract.md §2.3（Lead 冻结）。本脚本只做一件事：
把「这家供应商哪些协议有原生端点」这一事实从本机 Tingly Box registry 抄进目录，
供 /api/admin/model-sources 的 protocols 徽标使用。

为什么 openai 端点取自 catalog 自己的 base_url，而不是 registry 的 base_url_openai：
契约钉死的不变量是 `endpoints.openai ≡ base_url`。实测两者有 2 处不同（x-ai 多一个
尾斜杠、dashscope-intl-coding 域名不同），若这里抄 registry 的值，同一份数据里就会出现
两个真值来源，而 base_url 是贡献入口 DTO 的契约字段（contribution_v2_contract_test.go
断言 protocol=="openai"），一个字节都不能动。冲突以 catalog 为准，差异由脚本打印出来
交人工裁决，而不是由脚本偷偷选一个。

为什么整文件重写而不是行级补丁：json.dumps(indent=1, ensure_ascii=False) 与仓库里
现有 catalog.json 逐字节同形（缩进 1 空格、无 ASCII 转义、末尾换行），所以重写是无损的；
行级补丁反而会在有人手工调过缩进时写坏文件。脚本可重跑且幂等（重复执行得到同一文件）。

为什么是 Python 而不是 Go：这是数据生成脚本，不进构建、不进运行期，Go 里多一个 main
包只会让 `go vet ./...` 多背一份需要维护的代码。数据更新由 Lead 统一提交。

用法：
    python3 scripts/gen-apidir-endpoints.py [registry.json] [catalog.json]
默认路径：~/.local/share/tingly-box/provider_template.json 与 internal/apidir/catalog.json
"""

import json
import os
import sys

REGISTRY_DEFAULT = os.path.expanduser("~/.local/share/tingly-box/provider_template.json")
CATALOG_DEFAULT = os.path.join(
    os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
    "internal", "apidir", "catalog.json",
)

# endpoints 插在 api_doc 之后、models 之前：把「端点事实」这一组挨着放，
# 人在 diff 里能一眼看出只有这一类字段被动过。
INSERT_AFTER = "api_doc"


def load_json(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def build_endpoints(provider, registry):
    """返回该条目的 endpoints 对象（键序固定 openai → anthropic）。"""
    pid = provider["id"]
    base = provider["base_url"]
    out = {"openai": base}

    reg = registry.get(pid)
    if reg is not None:
        # 只对照不覆盖：registry 的 base_url_openai 与目录不一致时打印告警。
        if reg.get("base_url_openai") not in (None, base):
            warn(pid, "base_url_openai", reg.get("base_url_openai"), base)
        anthropic = (reg.get("base_url_anthropic") or "").strip()
        if anthropic:
            out["anthropic"] = anthropic
    return out


def warn(pid, field, registry_value, catalog_value):
    sys.stderr.write(
        "警告：%s 的 %s 与目录不一致 —— registry=%r，目录=%r；以目录为准（契约 §2.3 不变量）\n"
        % (pid, field, registry_value, catalog_value)
    )


def with_endpoints(provider, registry):
    """按原键序重建 provider，仅在 api_doc 之后插入 endpoints。"""
    out = {}
    inserted = False
    for key, value in provider.items():
        if key == "endpoints":
            continue  # 重跑时先剔旧值，避免插到两个位置
        out[key] = value
        if key == INSERT_AFTER:
            out["endpoints"] = build_endpoints(provider, registry)
            inserted = True
    if not inserted:
        # 目录里没 api_doc 的条目（未来新增）兜底：插在末尾，不静默丢弃端点事实。
        out["endpoints"] = build_endpoints(provider, registry)
    return out


def main():
    registry_path = sys.argv[1] if len(sys.argv) > 1 else REGISTRY_DEFAULT
    catalog_path = sys.argv[2] if len(sys.argv) > 2 else CATALOG_DEFAULT

    if not os.path.exists(registry_path):
        sys.stderr.write("找不到 Tingly Box registry：%s\n" % registry_path)
        sys.stderr.write("它只用来判定「这家有没有 anthropic 端点」，缺失时不猜、直接退出。\n")
        return 2

    registry = load_json(registry_path)["registry"]["providers"]
    catalog = load_json(catalog_path)
    providers = catalog["providers"]

    count_before = catalog["count"]
    if count_before != len(providers):
        sys.stderr.write("catalog.json 的 count=%d 与 providers 条数 %d 不一致，先修数据\n"
                         % (count_before, len(providers)))
        return 2

    order_before = [p["id"] for p in providers]
    catalog["providers"] = [with_endpoints(p, registry) for p in providers]

    # 自检：顺序、条数、老字段逐字节不变；endpoints.openai 恒等于 base_url。
    assert [p["id"] for p in catalog["providers"]] == order_before, "provider 顺序被改动了"
    assert catalog["count"] == len(catalog["providers"]), "count 被改动了"
    for p in catalog["providers"]:
        assert p["endpoints"]["openai"] == p["base_url"], "%s 的 openai 端点 != base_url" % p["id"]
        assert p["protocol"] == "openai", "%s 的 protocol 被改动了" % p["id"]

    text = json.dumps(catalog, indent=1, ensure_ascii=False) + "\n"
    with open(catalog_path, "w", encoding="utf-8") as f:
        f.write(text)

    matched = sum(1 for p in catalog["providers"] if p["id"] in registry)
    with_anthropic = sum(1 for p in catalog["providers"] if "anthropic" in p["endpoints"])
    print("目录条目 %d 条；registry 命中 %d 条；带 anthropic 端点 %d 条（改数据要同步改 "
          "apidir_test.go 的条目数断言）" % (len(catalog["providers"]), matched, with_anthropic))
    print("已写入 %s" % catalog_path)
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""FreeRouter 设计文档变更检测。

背景：设计文档（docs/**.md）在实现过程中**会持续变化**——新增功能、修改语义、
删除已否定的设计。代码分支落后于文档是本项目最主要的一类返工来源。

本工具做三件事：
  1. 记录/对比文档指纹（内容哈希 + 标题结构），回答「哪些文档变了」；
  2. 把变化映射到**受影响的分支与代码目录**（映射表在 DOC_OWNERS）；
  3. --brief 输出一段可以直接贴给队友的提醒文本。

用法：
    python3 scripts/docs_fingerprint.py baseline     # 记录当前基线
    python3 scripts/docs_fingerprint.py check        # 对比基线，输出变化（默认）
    python3 scripts/docs_fingerprint.py check --brief
    python3 scripts/docs_fingerprint.py check --json

基线存放在 .cache/docs-fingerprint.json（不入库：它是本地开发状态）。
退出码：0 = 无变化；1 = 有变化（便于在 CI 或 git hook 里用）；2 = 用法错误。
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
DOCS = REPO / "docs"
BASELINE = REPO / ".cache" / "docs-fingerprint.json"

# 文档 → (负责分支, 代码目录) 的映射。
#
# 为什么手工维护而不是自动推断：文档标题里不会写「这影响 internal/store」，
# 靠关键词猜测会把「账户与鉴权」错配到 meter。这张表就是人工判断的固化，
# 文档大改时更新它。
DOC_OWNERS: dict[str, tuple[list[str], list[str]]] = {
    # 总纲与清单类：影响所有人，但不直接归某个分支
    "PLAN.md": (["*"], ["*"]),
    "README.md": (["*"], ["*"]),
    "DECISIONS-REVIEW.md": (["*"], ["*"]),
    "OPEN-QUESTIONS.md": (["*"], ["*"]),
    "RESEARCH-UPSTREAM-REUSE.md": (["sidecar"], ["internal/upstream/", "sidecar/"]),

    # 架构
    "architecture/00-diagrams.md": (["*"], ["*"]),
    "architecture/01-overview.md": (["*"], ["internal/httpapi/", "cmd/"]),
    "architecture/02-ir.md": (["ir-protocol"], ["internal/ir/"]),
    "architecture/03-upstream.md": (["sidecar"], ["internal/upstream/"]),
    "architecture/04-sidecar.md": (["sidecar"], ["internal/upstream/dshprovider/", "sidecar/"]),
    "architecture/05-storage.md": (["storage"], ["internal/store/"]),
    "architecture/06-runtime.md": (["ir-protocol", "authn"], ["internal/httpapi/", "internal/meter/"]),

    # 权限与安全
    "security/01-accounts.md": (["authn", "storage"], ["internal/auth/", "internal/store/"]),
    "security/02-authn.md": (["authn"], ["internal/auth/"]),
    "security/03-groups.md": (["authn", "storage"], ["internal/auth/", "internal/store/"]),
    "security/04-authz.md": (["authn"], ["internal/auth/"]),
    "security/05-keys.md": (["authn", "storage"], ["internal/auth/", "internal/store/"]),
    "security/06-audit.md": (["storage", "authn"], ["internal/store/", "internal/auth/"]),

    # 模型体系
    "models/01-catalog.md": (["storage"], ["internal/store/"]),
    "models/02-abstract.md": (["ir-protocol", "storage"], ["internal/ir/", "internal/store/"]),
    "models/03-strategy.md": (["authn", "ir-protocol"], ["internal/meter/", "internal/ir/"]),
    "models/04-deprecation.md": (["storage"], ["internal/store/"]),
    "models/05-verification.md": (["sidecar"], ["internal/upstream/"]),

    # 贡献与凭据
    "contributions/01-flow.md": (["sidecar", "authn"], ["sidecar/", "internal/auth/"]),
    "contributions/02-credentials.md": (["sidecar", "storage"], ["sidecar/", "internal/store/"]),

    # 界面
    "ui/01-ia.md": (["web"], ["web/"]),
    "ui/02-public.md": (["web"], ["web/"]),
    "ui/03-admin.md": (["web"], ["web/"]),
    "ui/04-tenant.md": (["web"], ["web/"]),
    "ui/05-contributor.md": (["web"], ["web/"]),
    "ui/06-design.md": (["web"], ["web/"]),

    # API 契约
    "api/01-inbound.md": (["ir-protocol"], ["internal/ir/", "internal/httpapi/"]),
    "api/02-admin.md": (["*"], ["internal/httpapi/"]),
    "api/03-errors.md": (["*"], ["internal/httpapi/errors.go"]),

    # 运维
    "ops/01-security.md": (["*"], ["internal/httpapi/"]),
    "ops/02-deploy.md": (["*"], ["internal/config/"]),
    "ops/03-milestones.md": (["*"], ["*"]),
    "ops/04-collaboration.md": (["*"], ["*"]),
}

HEADING_RE = re.compile(r"^(#{1,6})\s+(.*\S)\s*$")


def iter_docs() -> list[Path]:
    if not DOCS.is_dir():
        return []
    return sorted(p for p in DOCS.rglob("*.md") if p.is_file())


def rel(p: Path) -> str:
    return str(p.relative_to(DOCS)).replace("\\", "/")


def headings(text: str) -> list[str]:
    """只取标题文本，保留层级；用于「结构变了」的粗粒度判断。"""
    out: list[str] = []
    in_fence = False
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith("```"):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        m = HEADING_RE.match(line)
        if m:
            out.append(f"{m.group(1)} {m.group(2).strip()}")
    return out


def fingerprint(path: Path) -> dict:
    raw = path.read_bytes()
    text = raw.decode("utf-8", errors="replace")
    hs = headings(text)
    return {
        "sha256": hashlib.sha256(raw).hexdigest(),
        "bytes": len(raw),
        "lines": text.count("\n") + (0 if text.endswith("\n") else 1),
        "headings": hs,
    }


def scan() -> dict[str, dict]:
    return {rel(p): fingerprint(p) for p in iter_docs()}


def load_baseline() -> dict | None:
    if not BASELINE.is_file():
        return None
    try:
        return json.loads(BASELINE.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return None


def save_baseline(data: dict) -> None:
    BASELINE.parent.mkdir(parents=True, exist_ok=True)
    BASELINE.write_text(
        json.dumps({"version": 1, "docs": data}, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


def diff(before: dict[str, dict], after: dict[str, dict]) -> dict:
    """返回结构化的差异：新增 / 删除 / 修改（含标题级变化）。"""
    added = sorted(set(after) - set(before))
    removed = sorted(set(before) - set(after))

    modified = []
    for name in sorted(set(before) & set(after)):
        b, a = before[name], after[name]
        if b["sha256"] == a["sha256"]:
            continue
        bh, ah = b.get("headings", []), a.get("headings", [])
        added_h = [h for h in ah if h not in bh]
        removed_h = [h for h in bh if h not in ah]
        modified.append(
            {
                "doc": name,
                "bytes_delta": a["bytes"] - b["bytes"],
                "lines_delta": a["lines"] - b["lines"],
                "added_headings": added_h,
                "removed_headings": removed_h,
            }
        )
    return {"added": added, "removed": removed, "modified": modified}


def owners_for(doc: str) -> tuple[list[str], list[str]]:
    if doc in DOC_OWNERS:
        return DOC_OWNERS[doc]
    # 未登记的文档（用户新加的分册）：按目录前缀找最接近的已登记文档。
    for known, val in DOC_OWNERS.items():
        if doc.split("/")[0] == known.split("/")[0]:
            return val
    return (["*"], ["*"])


def render(d: dict, brief: bool = False) -> str:
    if not (d["added"] or d["removed"] or d["modified"]):
        return "文档无变化。" if brief else "✓ 设计文档自上次基线以来无变化。"

    branches: dict[str, list[str]] = {}
    dirs: set[str] = set()
    for doc in d["added"] + d["removed"] + [m["doc"] for m in d["modified"]]:
        bs, ds = owners_for(doc)
        for b in bs:
            branches.setdefault(b, []).append(doc)
        dirs.update(ds)

    if brief:
        lines = ["【设计文档有变更，请对照调整你的实现】"]
        lines.append(
            "改动文件：" + ", ".join(
                d["added"] + d["removed"] + [m["doc"] for m in d["modified"]]
            )
        )
        for b, docs in sorted(branches.items()):
            lines.append(f"- 分支 {b}：受影响文档 {', '.join(sorted(set(docs)))}")
        lines.append(f"- 涉及代码目录：{', '.join(sorted(dirs))}")
        lines.append(
            "要求：以最新文档为准改实现（已实现的按其重构，未实现的按新语义实现），"
            "合并进 main 前必须重新核对一遍。"
        )
        return "\n".join(lines)

    out: list[str] = []
    if d["added"]:
        out.append("新增文档：")
        out.extend(f"  + {x}" for x in d["added"])
    if d["removed"]:
        out.append("删除文档：")
        out.extend(f"  - {x}" for x in d["removed"])
    if d["modified"]:
        out.append("修改文档：")
        for m in d["modified"]:
            out.append(
                f"  * {m['doc']}  ({m['lines_delta']:+d} 行, {m['bytes_delta']:+d} 字节)"
            )
            for h in m["added_headings"][:12]:
                out.append(f"      + 章节 {h}")
            for h in m["removed_headings"][:12]:
                out.append(f"      - 章节 {h}")
            if len(m["added_headings"]) > 12 or len(m["removed_headings"]) > 12:
                out.append("      …（章节变化过多，建议直接看 diff）")

    out.append("")
    out.append("受影响的分支与目录：")
    for b, docs in sorted(branches.items()):
        out.append(f"  [{b}] ← {', '.join(sorted(set(docs)))}")
    out.append(f"  代码目录：{', '.join(sorted(dirs))}")
    out.append("")
    out.append("建议：git diff -- docs/ 看全文差异，必要时用 send_message 通知对应队友。")
    return "\n".join(out)


def main() -> int:
    ap = argparse.ArgumentParser(description="FreeRouter 设计文档变更检测")
    ap.add_argument("action", choices=["baseline", "check", "show"], help="baseline=记录基线，check=对比，show=打印基线摘要")
    ap.add_argument("--brief", action="store_true", help="输出可直接贴给队友的简短提醒")
    ap.add_argument("--json", action="store_true", help="以 JSON 输出差异")
    args = ap.parse_args()

    current = scan()
    if not current:
        print(f"未找到设计文档：{DOCS}", file=sys.stderr)
        return 2

    if args.action == "baseline":
        save_baseline(current)
        total_lines = sum(v["lines"] for v in current.values())
        print(f"已记录基线：{len(current)} 个文档，共 {total_lines} 行 → {BASELINE.relative_to(REPO)}")
        return 0

    if args.action == "show":
        base = load_baseline()
        if base is None:
            print("尚无基线，请先运行 baseline。", file=sys.stderr)
            return 2
        docs = base.get("docs", {})
        for name in sorted(docs):
            print(f"{docs[name]['lines']:>5} 行  {name}")
        print(f"\n共 {len(docs)} 个文档，{sum(v['lines'] for v in docs.values())} 行")
        return 0

    base = load_baseline()
    if base is None:
        print("尚无基线，请先运行：python3 scripts/docs_fingerprint.py baseline", file=sys.stderr)
        return 2

    d = diff(base.get("docs", {}), current)
    if args.json:
        print(json.dumps(d, ensure_ascii=False, indent=2))
    else:
        print(render(d, brief=args.brief))
    return 1 if (d["added"] or d["removed"] or d["modified"]) else 0


if __name__ == "__main__":
    sys.exit(main())

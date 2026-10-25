#!/usr/bin/env python3
"""把 JetBrains Maple Mono 子集化成界面用的 woff2。

## 为什么要子集化

上游字体每个字重 18.5 MB（含全量中日韩 20975 字 + Nerd Font 几千个图标字形），
16 个字重共 294 MB。直接塞进网页不可能——单二进制会从 23 MB 涨到 300 MB。

但界面其实只用到 **1500 个左右的字符**（中文文案 + 拉丁 + 符号）。按实际字符集
裁一遍，每个字重降到 ~870 KB，三个字重合计约 2.6 MB，可以接受。

## 字符集怎么定

1. 扫 `web/src/**` 与 `internal/**`、`cmd/**` 里出现的**全部字符**——
   界面文案、后端错误消息（会显示在页面上）都在里面；
2. 并入 ASCII 可打印区与常用标点/箭头/框线；
3. 并入一份常用汉字表（GB2312 一级字），覆盖用户输入的名字、模型名里
   大概率会出现的字。

**超出子集的字符会回退到系统字体**（这是 `font-family` 栈的正常行为），
所以生僻字不会变成豆腐块，只是字形风格不一致。

## 用法

    cd web && python3 scripts/build-fonts.py

依赖 `fonttools`（`pip install fonttools brotli`；brotli 是写出 woff2 必需的）。
脚本把产物写到 `web/src/assets/fonts/`，Vite 构建时按内容哈希输出到 assets/，
最终被 `go:embed` 打进二进制。

## 许可证

字体是 **SIL Open Font License 1.1**（OFL-1.1），允许嵌入与再分发，
要求随附许可证副本——见同目录的 `OFL.txt`（脚本会一并写出）。

字体本身是 JetBrains Mono、Maple Mono、Fusion-JetBrainsMapleMono 三者的融合：
    Copyright 2020 The JetBrains Mono Project Authors
    Copyright 2022 The Maple Mono Project Authors
    Copyright 2025 Space Time (Fusion-JetBrainsMapleMono)
"""

from __future__ import annotations

import argparse
import glob
import os
import sys
import time

try:
    from fontTools.ttLib import TTFont
    from fontTools import subset
except ImportError:  # pragma: no cover
    sys.exit("需要 fonttools：pip install fonttools brotli")

HERE = os.path.dirname(os.path.abspath(__file__))
WEB = os.path.dirname(HERE)
REPO = os.path.dirname(WEB)
OUT_DIR = os.path.join(WEB, "src", "assets", "fonts")

# 上游字重 → 我们要输出的档位。
#
# 为什么只要三档：界面实际用到 400/500/600/650/700/800，但 CSS 的字体匹配
# 会把 500 落到 400、650 落到 700，所以真正的视觉档位只有三档。
# 每多一档就是 +870 KB，收在这里是值得的。
WEIGHTS = [
    ("Regular", 400),
    ("SemiBold", 600),
    ("Bold", 700),
]

# 上游字体安装目录（本机由 AUR/发行版包提供）。可用 --font-dir 覆盖。
DEFAULT_FONT_DIRS = [
    "/usr/share/fonts/TTF/JetBrains-Maple-Mono-NF-XX-XX",
    "/usr/share/fonts/truetype/JetBrains-Maple-Mono-NF-XX-XX",
    os.path.expanduser("~/.local/share/fonts/JetBrains-Maple-Mono-NF-XX-XX"),
]

# 常用汉字（GB2312 一级字表，3500 字）。覆盖用户输入的人名、模型名里的常见字。
COMMON_HANZI = (
    "的一是在不了有和人这中大为上个国我以要他时来用们生到作地于出就分对成会可主发年动同工也能下过子说产种面而方后多定行学法所民得经十三之进着等部度家电力里如水化高自二理起小物现实加量都两体制机当使点从业本去把性好应开它合还因由其些然前外天政四日那社义事平形相全表间样与关各重新线内数正心反你明看原又么利比或但质气第向道命此变条只没结解问意建月公无系军很情者最立代想已通并提直题党程展五果料象员革位入常文总次品式活设及管特件长求老头基资边流路级少图山统接知较将组见计别她手角期根论运农指几九区强放决西被干做必战先回则任取据处队南给色光门即保治北造百规热领七海口东导器压志世金增争济阶油思术极交受联什认六共权收证改清己美再采转更单风切打白教速花带安场身车例真务具万每目至达走积示议声报斗完类八离华名确才科张信马节话米整空元况今集温传土许步群广石记需段研界拉林律叫且究观越织装影算低持音众书布复容儿须际商非验连断深难近矿千周委素技备半办青省列习响约支般史感劳便团往酸历市克何除消构府称太准精值号率族维划选标写存候毛亲快效斯院查江型眼王按格养易置派层片始却专状育厂京识适属圆包火住调满县局照参红细引听该铁价严龙飞"
)

# 界面会用到的符号：箭头、框线、常见标点、数学与状态符号。
EXTRA_SYMBOLS = "←→↑↓…—–·•✓✔✗✘×÷±×§¶†‡「」『』〈〉《》【】〔〕（）［］｛｝、。，；：？！“”‘’～＃＆＊＠"


def collect_chars() -> set[str]:
    """扫源码与后端文案，得到界面实际会用到的全部字符。"""
    chars: set[str] = set()
    patterns = [
        os.path.join(WEB, "src", "**", "*.tsx"),
        os.path.join(WEB, "src", "**", "*.ts"),
        os.path.join(REPO, "internal", "**", "*.go"),
        os.path.join(REPO, "cmd", "**", "*.go"),
    ]
    for pat in patterns:
        for path in glob.glob(pat, recursive=True):
            with open(path, encoding="utf-8") as fh:
                chars |= set(fh.read())

    # 控制字符不参与（它们在字体里没有字形，留着只会让子集命令报错）
    chars -= {"\n", "\r", "\t", "\u200b", "\ufeff"}
    # ASCII 可打印区
    chars |= {chr(c) for c in range(0x20, 0x7F)}
    chars |= set(EXTRA_SYMBOLS)
    chars |= set(COMMON_HANZI)
    return chars


def find_font_dir(explicit: str | None) -> str:
    if explicit:
        if not os.path.isdir(explicit):
            sys.exit(f"--font-dir 不存在：{explicit}")
        return explicit
    for d in DEFAULT_FONT_DIRS:
        if os.path.isdir(d):
            return d
    sys.exit(
        "找不到 JetBrains Maple Mono 的安装目录。\n"
        "请从 https://github.com/SpaceTimee/Fusion-JetBrainsMapleMono 下载\n"
        "JetBrainsMapleMono-NF-XX-XX.zip 解压后，用 --font-dir 指向它。"
    )


def build(font_dir: str, chars: set[str]) -> None:
    os.makedirs(OUT_DIR, exist_ok=True)
    unicodes = {ord(c) for c in chars}
    hanzi = sum(1 for u in unicodes if 0x4E00 <= u <= 0x9FFF)
    print(f"字符集：{len(unicodes)} 个（其中汉字 {hanzi} 个）")

    total = 0
    for upstream, weight in WEIGHTS:
        src = os.path.join(font_dir, f"JetBrainsMapleMono-{upstream}.ttf")
        if not os.path.isfile(src):
            sys.exit(f"缺字体文件：{src}")
        t0 = time.time()
        font = TTFont(src)
        opts = subset.Options()
        opts.flavor = "woff2"
        # 保留全部 OpenType 特性：等宽字体的连字（calt）与定位（kern）都在这里，
        # 裁掉的话 `->` 之类的连字会消失，字体特有的观感就丢了。
        opts.layout_features = ["*"]
        opts.notdef_outline = True
        sub = subset.Subsetter(options=opts)
        sub.populate(unicodes=unicodes)
        sub.subset(font)

        out = os.path.join(OUT_DIR, f"jbm-{weight}.woff2")
        font.save(out)
        kb = os.path.getsize(out) / 1024
        total += kb
        print(f"  {upstream:<9} → jbm-{weight}.woff2  {kb:6.0f} KB  ({time.time() - t0:.1f}s)")

    write_license(font_dir)
    print(f"合计 {total / 1024:.2f} MB → {os.path.relpath(OUT_DIR, REPO)}")


def write_license(font_dir: str) -> None:
    """OFL-1.1 要求再分发时随附许可证副本。"""
    text = """JetBrains Maple Mono — SIL Open Font License 1.1

本目录下的 jbm-*.woff2 是 JetBrains Maple Mono 的**子集**（按界面字符裁剪），
仅用于 FreeRouter 控制台。未修改字形轮廓，仅移除未使用的字符。

字形版权：
  Copyright 2020 The JetBrains Mono Project Authors (https://github.com/JetBrains/JetBrainsMono)
  Copyright 2022 The Maple Mono Project Authors (https://github.com/subframe7536/maple-font)
  Copyright 2025 Space Time (https://github.com/SpaceTimee/Fusion-JetBrainsMapleMono)

来源：https://github.com/SpaceTimee/Fusion-JetBrainsMapleMono

许可：SIL Open Font License, Version 1.1 —— 全文见
https://openfontlicense.org 或本目录的 OFL-1.1.txt

重新生成：cd web && python3 scripts/build-fonts.py
"""
    with open(os.path.join(OUT_DIR, "LICENSE-fonts.txt"), "w", encoding="utf-8") as fh:
        fh.write(text)

    # 上游发行包若自带 OFL 全文则一并复制；没有就只留上面的声明（含官方链接）。
    for name in ("OFL.txt", "OFL-1.1.txt", "LICENSE.txt"):
        candidate = os.path.join(font_dir, name)
        if os.path.isfile(candidate):
            with open(candidate, encoding="utf-8") as src, open(
                os.path.join(OUT_DIR, "OFL-1.1.txt"), "w", encoding="utf-8"
            ) as dst:
                dst.write(src.read())
            return


def main() -> None:
    ap = argparse.ArgumentParser(description="生成界面字体子集（woff2）")
    ap.add_argument("--font-dir", help="JetBrains Maple Mono 的 TTF 所在目录")
    ap.add_argument("--chars-only", action="store_true", help="只打印字符集统计，不生成字体")
    args = ap.parse_args()

    chars = collect_chars()
    if args.chars_only:
        hanzi = sum(1 for c in chars if 0x4E00 <= ord(c) <= 0x9FFF)
        print(f"字符集：{len(chars)} 个（其中汉字 {hanzi} 个）")
        return
    build(find_font_dir(args.font_dir), chars)


if __name__ == "__main__":
    main()

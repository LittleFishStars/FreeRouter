# 界面字体（JetBrains Maple Mono 子集）

这里的 `jbm-*.woff2` 是 **JetBrains Maple Mono** 的子集文件，由
`web/scripts/build-fonts.py` 生成。**它们是构建输入，必须入库**：

- 项目是单二进制分发（`go:embed`），字体是构建必需品；
- 不入库的话，每个构建环境都得先装同一版本的字体 + fonttools，构建不可复现；
- 三个文件合计 2.6 MB，换来 `git clone && make release` 直接可构建。

上游字体每个字重 18.5 MB（全量 CJK + Nerd Font 图标），必须裁剪才能内嵌。

## 重新生成

源码文案变动后（新增了生僻字），重跑：

```bash
cd web && python3 scripts/build-fonts.py
```

依赖 `fonttools` + `brotli`。脚本会按当前源码里出现的**全部字符**重新裁剪，
并更新同目录的 `LICENSE-fonts.txt`。

## 许可证

SIL Open Font License 1.1，见 [`LICENSE-fonts.txt`](LICENSE-fonts.txt)。
字形版权归 JetBrains Mono / Maple Mono / Fusion-JetBrainsMapleMono 三个上游项目所有。

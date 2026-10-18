# XRouter 控制台（webui）

XRouter 管理面板的前端源码。构建产物是**单文件** `index.html`，由 Go 后端从
`static/index.html` 直接读盘提供（不下载、不自动更新）。

面板基于 **v8 Management API**（`/v8/management`）与 v8 配置布局工作，没有 v0 回退。

## 来源

源码 vendor 自上游控制台项目，已按 XRouter 品牌改造：

- 上游仓库：<https://github.com/router-for-me/Cli-Proxy-API-Management-Center>
- 上游提交：`fe2963330f32a933f562282165f62ae52cf57145`
  （2026-10-16，`feat(quota): add logic to determine visibility of quota availability for Antigravity buckets`）
- 许可证：MIT，见 `LICENSE`

vendor 时的取舍：

- **不保留上游 git 记录**，只保留文件内容，便于随 XRouter 一起提交与审查。
- **不复制上游 `AGENTS.md`**：本仓 CI 会拦截任何 `AGENTS.md` / `*/AGENTS.md` 改动。
- 不带入上游 README 的推广图片（`assets/apimart-*.png`）与 `logo.jpg`。

因此后续跟进上游更新只能**手工 diff**（以上游提交为基线），无法直接 `git merge`。

## 品牌化改动

- 品牌文案：`CLI Proxy API` / `CLIProxyAPI` / `CPAMC` → `XRouter`。
- 图标：`src/assets/logoInline.ts` 改为 XRouter 品牌图标的 SVG data URI
  （来源 `../assets/brand/xrouter-icon.svg` 与 `xrouter-icon-light.svg`），
  用于 favicon、侧边栏与登录页，并跟随 azure 浅色/深色主题切换。
- 仓库链接改指向 <https://github.com/xnuyoah/XRouter>。
- 本地加密存储盐值改为 `xrouter-webui::secure-storage`。
- 移除上游的推广返利参数（Kimi affiliate `?aff=cliproxyapi`）。
- 插件信任提示中的官方组织名改为 `xnuyoah`；插件市场等 owner/repo 路径保持不变。

## 构建

需要 [Bun](https://bun.sh) 1.3.x。仓库的 CI 与镜像**不构建**前端，产物直接入库。

```bash
bun install --frozen-lockfile
bun run type-check              # tsc --noEmit
bun run build                   # tsc && vite build -> dist/index.html
cp dist/index.html ../static/index.html
```

`vite-plugin-singlefile` 会把 JS 与 CSS 全部内联，所以 `dist/index.html` 是唯一需要
分发的产物（约 3 MB）。构建完请一并提交 `static/index.html`。

## 开发

```bash
bun run dev                     # 启动 Vite 开发服务器
bun run test                    # bun test
bun run lint                    # eslint
bun run verify                  # test + lint + build
```

开发服务器默认连不上后端管理接口，请在登录页填写 XRouter 的服务地址与管理密钥。

## 本地化

翻译在 `src/i18n/locales/`（`zh-CN`、`zh-TW`、`en`、`ru`、`vi`、`ko`），
语言回退为 `zh-CN`。新增界面文案需要为每种语言补齐对应键。

## 新增模块：搜索渠道

`src/features/webSearch/` 是 XRouter 新增的搜索渠道模块
（tavily / exa / anysearch / firecrawl / tinyfish），对接后端
`/v8/management/web-search/*`，使用说明见仓库根目录的 `docs/web-search.md`。

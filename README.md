# XRouter

<img src="./assets/brand/xrouter-glyph.svg" alt="XRouter" width="44" height="44">

XRouter 是一个本地代理服务器，对 CLI 及其他客户端暴露 **OpenAI 兼容**与 **Claude 兼容**的 API 接口，后端可以接多家上游供应商，并支持用多个凭据做轮询负载均衡。

任何兼容 OpenAI（含 Responses）或 Claude 的客户端 / SDK，都可以只改 base URL 指向本机即可接入。

## 支持的供应商

| 供应商 | 凭据方式 | 默认端点 | 说明 |
| --- | --- | --- | --- |
| OpenAI | 原生 API Key | `https://api.openai.com/v1` | 未声明模型时使用内置目录（`gpt-5.6-luna`、`gpt-6-luna`） |
| DeepSeek | 原生 API Key | `https://api.deepseek.com/v1` | 未声明模型时使用内置目录（`deepseek-flash`，客户端别名 `deepseek-v4.1-flash`） |
| NVIDIA NIM | 原生 API Key | `https://integrate.api.nvidia.com/v1` | 不内置模型目录，需在配置中声明要服务的模型 |
| Agnes AI | 原生 API Key | `https://apihub.agnes-ai.com/v1` | 默认目录 `agnes-3.0-flash`、`agnes-2.5-flash`，512K 上下文，支持文本 + 图片输入 |
| 第三方兼容上游 | 原生 API Key | 自定义 `base-url` | 通过 `openai-compatibility` 通用机制接入（例如 OpenRouter 等） |

## 主要能力

- OpenAI 兼容端点：对话补全、文本补全、Responses（含紧凑变体）
- Claude 兼容端点：Messages 与 token 计数
- 流式、非流式，以及 Responses 的 WebSocket 响应
- 函数调用 / 工具调用
- 多模态输入（文本与图片）
- 多个凭据的轮询负载均衡（round-robin、加权轮询、fill-first）
- 原生 API Key 家族：`openai`、`deepseek`、`nvidia`、`agnes`
- 通用的 `openai-compatibility` 上游配置，可接入任意兼容供应商
- 搜索渠道：`tavily` / `exa` / `anysearch` / `firecrawl` / `tinyfish` 的透传网关、MCP 镜像、账号池选号、测活、渠道状态与额度读取（见 `docs/web-search.md`）
- 观测：内置 LLM Token 用量与成本估算、搜索工具用量分析，支持按模型/供应商/渠道/操作/计划/额度维度聚合（见 `docs/usage-analytics.md`）
- 动态库插件体系，以及内置的管理面板
- 可嵌入的 Go SDK（见 `docs/sdk-usage.md`）

## 快速开始

### 1. 准备配置

```bash
cp config.example.yaml config.yaml
```

需要改的两处：

- `access.api-keys` —— **客户端**访问本代理时使用的鉴权 key（不是上游供应商的 key）
- `api-keys.*` —— 上游**供应商**的凭据

### 2. 配置供应商凭据

以原生 API Key 家族为例（`config.yaml`）：

```yaml
api-keys:
  openai:
    - name: openai-1
      keys:
        - api-key: "sk-..."
  deepseek:
    - name: deepseek-1
      keys:
        - api-key: "sk-..."
  # nvidia:
  #   - name: nvidia-1
  #     models:
  #       - name: "nvidia/llama-3.3-nemotron-super-49b-v1"
  #     keys:
  #       - api-key: "nvapi-..."
```

每个家族都可以覆盖 `base-url`、给单个 key 加 `weight`、用 `models` 声明该凭据对外暴露的模型（`name` 为上游模型名，`alias` 为客户端可见名）。未声明模型时回落到内置默认目录。

OAuth / 文件型凭据由插件提供，凭据文件存放在 `oauth.auth-dir`（默认 `~/.xrouter`）。

### 3. 启动

```bash
# 开发模式
go run ./cmd/server

# 构建后运行
go build -o xrouter ./cmd/server
./xrouter
```

默认监听 `11016` 端口。首次启动会自动加载当前工作目录下的 `.env`。

### 4. 客户端接入

把客户端的 base URL 指向 `http://127.0.0.1:11016/v1`，API key 填 `access.api-keys` 里的值即可。

### 5. 管理面板

访问 `http://127.0.0.1:11016/` 会跳转到管理面板 `http://127.0.0.1:11016/index.html`（旧地址 `/management.html` 保留为别名）。管理接口默认只允许本机访问，且必须配置 `management.secret-key` 才能使用。

## 命令行参数

| 参数 | 说明 |
| --- | --- |
| `--config <path>` | 指定配置文件路径（默认 `config.yaml`） |
| `--discover` | 发现局域网内的 AI 网关与实例（可选 `--discover-json`、`--discover-timeout`、`--discover-service-type`、`--discover-include`、`--discover-exclude`） |
| `--home-jwt <jwt>` | Home 控制面的 JWT，用于 mTLS 证书引导与连接 |
| `--home-disable-cluster-discovery` | 关闭 Home 集群节点发现，仅使用 `--home-jwt` 地址 |
| `--password <value>` | 覆盖管理密钥 |

## 协议端点

| 端点 | 说明 |
| --- | --- |
| `/v1/chat/completions`、`/v1/completions` | OpenAI 兼容对话 / 补全 |
| `/v1/responses`、`/v1/responses/compact` | OpenAI Responses |
| `/v1/messages`、`/v1/messages/count_tokens` | Claude 兼容 |
| `/v1/images/generations`、`/v1/images/edits` | 图像生成 / 编辑 |
| `/v1/models`、`/v1/models/*` | 模型列表与详情 |
| `/backend-api/codex/responses` | Codex CLI 兼容别名（指向 Responses） |
| `/gateway/<channel>/<path>` | 搜索渠道透传网关（复用 `access.api-keys`） |
| `/mcp/<channel>` | 搜索渠道 MCP 镜像端点（复用 `access.api-keys`） |
| `/v8/management/*` | 管理 API |
| `/healthz` | 健康检查 |
| `/` | 跳转到管理面板 |

## 配置概览

`config.yaml` 采用 v8 结构（`config-version: 8`），顶层分段：

- `models` —— 模型目录来源
- `server` —— 监听地址、端口、TLS、请求体大小上限等
- `management` —— 管理接口与管理面板
- `access` —— 客户端鉴权
- `routing` —— 凭据选择策略与重试
- `requests` —— 请求级行为（代理、payload 规则等）
- `oauth` —— OAuth / 文件型凭据（`auth-dir` 默认 `~/.xrouter`）
- `api-keys` —— 上游供应商凭据（`openai` / `deepseek` / `nvidia` / `agnes` / `openai-compatibility`）
- `multimedia` —— 内置图片生成行为
- `observability` —— 日志、用量统计、profiling
- `web-search` —— 搜索渠道（渠道、账号池、探测代理与调度）

完整的字段与注释见 [`config.example.yaml`](config.example.yaml)。

## 文档

- 管理 API：[docs/management-api-v8.md](docs/management-api-v8.md)
- 用量分析：[docs/usage-analytics.md](docs/usage-analytics.md)
- 搜索渠道：[docs/web-search.md](docs/web-search.md)
- SDK 使用：[docs/sdk-usage.md](docs/sdk-usage.md)
- SDK 进阶（executor 与 translator）：[docs/sdk-advanced.md](docs/sdk-advanced.md)
- SDK 访问控制：[docs/sdk-access.md](docs/sdk-access.md)
- SDK Watcher：[docs/sdk-watcher.md](docs/sdk-watcher.md)
- 自定义供应商示例：`examples/custom-provider`

## 构建与部署

```bash
gofmt -w .
go build -o xrouter ./cmd/server
go test ./...
```

Docker 镜像：`registry.cn-hangzhou.aliyuncs.com/xctr/xrouter`，可通过 `docker-compose.yml` 启动，配置、凭据与日志目录按需挂载为卷。生产上线前请按 [docs/production.md](docs/production.md) 完成 TLS、密钥、备份、监控和探测负载检查。

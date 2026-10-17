# 搜索渠道（Web Search）

XRouter 可以把搜索/抓取类服务商作为「渠道」接入，并统一做账号池轮询、测活、额度读取与
MCP 镜像。目前支持五个渠道：

| 渠道 | 用途 | 上游主机 | 鉴权头 | MCP |
| --- | --- | --- | --- | --- |
| `tavily` | 搜索 / 抽取 / 站点图 / 抓取 | `https://api.tavily.com` | `Authorization: Bearer` | 有 |
| `exa` | 搜索 / 内容 / 相似页 | `https://api.exa.ai` | `x-api-key` | 有 |
| `anysearch` | 子域 / 搜索 / 抽取 | `https://api.anysearch.com` | `Authorization: Bearer` | 有 |
| `firecrawl` | 抓取 / 搜索 / 站点图 / 爬取 | `https://api.firecrawl.dev` | `Authorization: Bearer` | 有 |
| `tinyfish` | 搜索 / 抓取 / 钱包 | 见下方「多域名」说明 | `x-api-key` | 有 |

这类渠道不提供对话模型，因此独立于 `api-keys` 的 LLM 供应商族，由配置的
`web-search` 段单独声明。

## 上游域名（多域名渠道）

Tinyfish 的搜索、抓取与钱包分属不同域名，XRouter 按路径与方法自动选择：

| 请求 | 上游主机 |
| --- | --- |
| `GET /` | `https://api.search.tinyfish.ai` |
| `POST /` | `https://api.fetch.tinyfish.ai` |
| `/v1/...`（含 `/v1/wallet`） | `https://agent.tinyfish.ai` |

在渠道上配置 `base-url` 可以整体覆盖上游主机，用于自建代理或灰度环境。

## 配置

```yaml
web-search:
  # 显式关闭整个子系统；留空且声明了渠道时即为开启。
  disabled: false
  # 探测（测活 / 渠道状态 / 额度刷新）使用的出站代理，支持 http、https、socks5、socks5h。
  # 为空时回退到顶层 requests.proxy-url；两者都为空时探测直接失败，绝不回退直连。
  proxy-url: "socks5://127.0.0.1:1080"
  gateway:
    # 单次请求最多尝试的账号数（额度不足 / 鉴权失效 / 限流时自动换号）。
    max-attempts: 3
  probe:
    interval-seconds: 3600   # 定时任务间隔：渠道状态检测 + 额度刷新
    concurrency: 4           # 探测并发上限
    timeout-seconds: 20      # 单次探测请求超时
    history-days: 90         # 渠道状态历史保留天数
  quota:
    fresh-seconds: 3600      # 额度快照的“新鲜”时长
  channels:
    - id: tavily
      name: Tavily
      disabled: false
      # base-url: "https://api.tavily.com"   # 可选：覆盖上游主机（不含路径）
      # proxy-url: "socks5://127.0.0.1:1080" # 可选：覆盖本渠道出站代理
      accounts:
        - name: "tavily-1"
          api-key: "tvly-..."
          weight: 1
          # disabled: false    # 可选：停用该账号
          # note: "预留"
    - id: anysearch
      name: AnySearch
      accounts:
        - name: "anysearch-1"
          api-key: "..."
          # AnySearch 的 API key 探测端点不返回余额，需填写网页登录账号密码。
          username: "login-id"
          password: "password"
```

**启用规则**：声明了至少一个渠道即视为开启。要临时关掉整个子系统但不删配置，把
`web-search.disabled` 设为 `true`。

渠道与账号的**唯一事实来源是 config.yaml**：管理面板上的新增/删除账号会写回该文件并触发
热重载，运行时状态（额度、冷却、可用性、调用日志）存放在独立的状态目录里，跨重载保留。

状态目录优先级：`WRITABLE_PATH/websearch` → 配置文件同级的 `websearch/` → 当前目录的
`websearch/`。

## 客户端接入

网关与 MCP 端点**复用 `access.api-keys` 中的客户端密钥**，不需要为每个渠道单独申请密钥：

- `Authorization: Bearer <access.api-keys>` 或 `x-api-key: <access.api-keys>`。
- Tavily 的 HTTP 网关额外接受请求体里的 `api_key` 字段。
- 查询参数中不允许出现凭据字段（`api_key`、`access_token` 等），出现即返回 400。

### HTTP 透传网关

```bash
curl -s http://127.0.0.1:11016/gateway/tavily/search \
  -H "Authorization: Bearer $XROUTER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"query":"golang","max_results":3}'
```

| 渠道 | 路径与来源 |
| --- | --- |
| `exa` | `POST /gateway/exa/search`、`/contents`、`/findSimilar` |
| `anysearch` | `GET /gateway/anysearch/v1/sub-domains`、`GET|POST /v1/search`、`POST /v1/extract` |
| `firecrawl` | `POST /gateway/firecrawl/v2/{scrape,search,map,crawl}`、`GET|DELETE /v2/crawl/{id}`、`GET /v2/team/credit-usage` |
| `tavily` | `POST /gateway/tavily/{search,extract,map,crawl}`、`GET /usage` |
| `tinyfish` | `GET /gateway/tinyfish/`（搜索）、`POST /gateway/tinyfish/`（抓取）、`GET /v1/wallet` |

透传语义是**原样转发**：路径、方法、查询串与请求体保持客户端原貌，XRouter 只做三件事：

1. 注入选中账号的上游密钥（按渠道替换鉴权头）；
2. 剥离请求体与查询串中的客户端凭据字段；
3. 回传响应前脱敏账号密钥并去掉 `Set-Cookie`；Firecrawl 的任务链接改写为网关地址。

网关会拒绝上游重定向（避免把凭据带到第三方），并对请求体设置 1 MiB 上限；
`Content-Type` 不是 JSON 时返回 415。

### MCP 镜像

每个渠道还提供一个 MCP 端点，逐字镜像该渠道官方的托管 MCP 工具：

```json
{
  "mcpServers": {
    "xrouter-tavily": {
      "url": "http://127.0.0.1:11016/mcp/tavily",
      "headers": { "Authorization": "Bearer $XROUTER_KEY" }
    }
  }
}
```

实现的是 MCP Streamable HTTP 的最小子集（无状态、JSON 响应）：`initialize`、
`notifications/*`、`ping`、`tools/list`、`tools/call`。工具清单来自上游官方 MCP
（游标分页，结果缓存 5 分钟）；工具调用使用同一套选号、冷却与额度刷新逻辑。
Tavily 的上游凭据走 `?tavilyApiKey=` 查询参数，Exa 走 `x-api-key`，其余走 Bearer。

## 选号与失败处理

同一渠道内串行处理请求，账号按以下顺序挑选（前 `gateway.max-attempts` 个依次尝试）：

1. 跳过停用、非「可用」状态、处于冷却期、以及剩余额度为 0 的账号；
2. 额度未知的排在最后，其余按剩余额度降序；
3. 剩余额度相同时，最久未使用的优先，最后按账号 ID 保证顺序稳定。

失败分类：

| 情况 | 处理 |
| --- | --- |
| 额度耗尽（402/403/429/432/433 + 错误文案命中） | 标记账号 `limited`，换下一个账号 |
| 429 限流 | 按 `Retry-After` 设置冷却（夹取到 1–300 秒，缺省 30 秒） |
| 401 / AnySearch 403 鉴权错误 | 标记账号 `expired`，换下一个账号 |
| 上游网络错误 | 换下一个账号 |
| 上游重定向 | 直接返回 502，不跟随 |
| 全部账号失败 | 返回 503 `quota_exhausted` |

Firecrawl 的异步任务（`POST /v2/crawl`）会把任务绑定到创建它的账号：后续的
`GET|DELETE /v2/crawl/{id}` 只会用同一个账号处理。绑定缺失或账号已停用/换密钥时，
返回 409/410 并提示重新创建任务。

## 测活、渠道状态与额度

**测活**对每个账号做一次真实探测（各渠道的探测端点见下表），并强制走代理：

| 渠道 | 探测端点 | 说明 |
| --- | --- | --- |
| `exa` | `GET /agent/runs?limit=1` | 只验证鉴权，不提供余额 |
| `anysearch` | `GET /v1/sub-domains?domain=code` | 余额需网页登录（见下） |
| `firecrawl` | `GET /v2/team/credit-usage` | 返回 `remainingCredits` |
| `tavily` | `GET /usage` | 返回 key / plan / paygo 三个维度 |
| `tinyfish` | `GET /v1/wallet` | 返回 `available_balance` |

判定规则：401 且响应是明确的 JSON 业务错误 → 鉴权失效；402 按渠道语义区分
（Exa 视为「额度未确认」，其余视为额度不足）；AnySearch 403 命中鉴权关键词 → 鉴权失效；
Tinyfish 404 表示钱包未开通（鉴权本身有效）；429 与 5xx **不下结论**，保留上一次状态。

**渠道状态**记录每个渠道的可用性并保留按日聚合的历史（默认 90 天）：

- 整体状态：任一渠道 `down` → `down`；任一 `degraded` → `degraded`；全部
  `operational` → `operational`；否则 `unknown`。
- HTTP 语义：401/402/403 → `unknown`（本机凭据或额度问题不能推断服务商全局状态）；
  429 → `degraded`；≥500 → `down`。
- 最近一次结果超过 24 小时会被强制标记为 `unknown` 并标注为过期。

**额度语义**：

- 只有 `anysearch`、`firecrawl`、`tavily`、`tinyfish` 提供可读额度；`exa` 不提供。
- **Firecrawl 允许负额度**：账号透支时上游返回负的 `remainingCredits`，面板按原始负数
  展示（红色标注），选号时按 0 处理（视为已耗尽）。
- Tinyfish 的 `available_balance` 同样可能为负，处理方式一致。
- Tavily 有 key / plan / paygo 三个维度：key 用尽、或 plan 与 paygo 同时用尽 → 额度不足；
  仅 plan 用尽但仍有 paygo 额度时仍视为可用。
- AnySearch 的余额必须通过网页控制台登录读取：配置 `username`/`password` 后，测活会
  登录 `www.anysearch.com` 读取 billing overview 并尽力登出。缺少登录凭据或登录失败时
  只记录说明，不影响 key 鉴权结论。
- 成功调用后会异步刷新额度（3 秒预算）；定时任务按 `probe.interval-seconds` 周期刷新，
  `quota.fresh-seconds` 内的快照会被跳过。

### 代理强制要求

**测活、渠道状态检测与额度刷新必须走代理**，没有可用代理时：

- 测活返回明确错误「未配置出站代理，搜索渠道探测不会回退直连」；
- 渠道状态记为 `unknown`，并附带「未配置探测代理，未发起请求（不会回退直连）」；
- 额度刷新记为失败，说明「未配置探测代理，未刷新额度（不会回退直连）」。

这是一条硬约束：探测请求不会回退直连。客户端透传网关则不受此限制——配置了代理就走代理，
未配置时按默认传输直连。

## 管理 API

搜索渠道的管理接口挂在 `/v8/management` 下，需要管理密钥：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/v8/management/web-search/channels` | 渠道列表与账号统计 |
| `GET` | `/v8/management/web-search/accounts` | 账号池（可带 `channel`、`status` 过滤；密钥只回显掩码） |
| `PUT`/`POST` | `/v8/management/web-search/accounts` | 新增或更新账号（写回 config.yaml） |
| `DELETE` | `/v8/management/web-search/accounts?channel=&account_id=` | 删除账号（写回 config.yaml） |
| `POST` | `/v8/management/web-search/probes` | 触发一轮测活（同步返回逐账号结果） |
| `GET` | `/v8/management/web-search/status` | 渠道状态快照 |
| `POST` | `/v8/management/web-search/status/check` | 触发渠道状态检测（异步；进行中返回 409） |
| `GET` | `/v8/management/web-search/monitoring` | 额度概览 + 渠道状态 |
| `POST` | `/v8/management/web-search/quota/refresh` | 立即刷新额度 |
| `GET` | `/v8/management/web-search/requests` | 最近调用日志（`channel`、`limit` 过滤） |

调用日志只记录方法、路径、状态码、尝试次数与耗时，不记录请求体、查询串与任何密钥。

## 管理面板

面板左侧「观测 → 搜索渠道」（`/web-search`）提供五个标签页：

- **概览**：账号指标卡、整体状态横幅、各渠道卡片（账号数、代理就绪、网关路径）、最近测活结果；
- **账号池**：状态筛选、密钥掩码、额度明细（负数额度红色显示）、额度重置时间、行内测活与删除、新增账号表单；
- **测活**：整渠道或按渠道触发测活并查看逐账号结果；
- **渠道状态**：手动检测、整体横幅、逐渠道的成功率/耗时/是否走代理、90 天历史色条；
- **接入说明**：各渠道的网关与 MCP 路径、curl 与 MCP 客户端配置示例、最近调用日志。

面板只展示与配置，不代替客户端发起业务调用。

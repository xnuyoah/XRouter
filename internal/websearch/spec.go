// Package websearch 实现搜索类渠道（tavily / exa / anysearch / firecrawl / tinyfish）
// 的接入：客户端透传网关、MCP 镜像、账号池选号、测活、渠道状态与额度刷新。
//
// 这些渠道不提供对话模型，只做搜索/抓取类 HTTP 透传，因此独立于 internal/runtime
// 下的 LLM executor 体系，由 config 的 web-search 段单独声明。
package websearch

import (
	"fmt"
	"strings"
)

// Channel 是搜索渠道标识，取值与 internal/constant 中的搜索渠道常量一致。
type Channel string

// 支持的搜索渠道。
const (
	ChannelTavily    Channel = "tavily"
	ChannelExa       Channel = "exa"
	ChannelFirecrawl Channel = "firecrawl"
	ChannelAnySearch Channel = "anysearch"
	ChannelTinyfish  Channel = "tinyfish"
)

// AllChannels 返回全部搜索渠道，顺序与面板展示顺序一致。
func AllChannels() []Channel {
	return []Channel{ChannelExa, ChannelAnySearch, ChannelFirecrawl, ChannelTavily, ChannelTinyfish}
}

// ParseChannel 解析渠道标识；未知渠道返回错误。
func ParseChannel(raw string) (Channel, error) {
	normalized := Channel(strings.ToLower(strings.TrimSpace(raw)))
	for _, candidate := range AllChannels() {
		if normalized == candidate {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("未知搜索渠道 %q", raw)
}

// authStyle 描述上游凭据的注入方式。
type authStyle int

const (
	// authBearer 使用 `Authorization: Bearer <key>`。
	authBearer authStyle = iota
	// authAPIKeyHeader 使用 `<header>: <key>`（目前都是 x-api-key）。
	authAPIKeyHeader
)

// pathHost 表示某个路径前缀需要改用不同的上游主机。
type pathHost struct {
	prefix string
	host   string
}

// operationSet 描述某个路径允许的 HTTP 方法。
type operationSet struct {
	path    string
	methods []string
}

// channelSpec 是单个渠道的静态规格：上游主机、操作白名单、鉴权方式、探测端点等。
// 所有值都来自上游服务商的官方接口约定，运行期不做任何推断。
type channelSpec struct {
	channel Channel
	// displayName 是面板与日志使用的展示名。
	displayName string
	// host 是默认上游源（scheme + host，不含路径）。
	host string
	// pathHosts 是按路径前缀覆盖的主机（Tinyfish 的搜索/抓取/钱包分属不同域名）。
	pathHosts []pathHost
	// postHost 是 POST 请求的默认主机覆盖（Tinyfish 的抓取域名）。
	postHost string
	// operations 是路径与方法白名单；不在此表内的请求一律拒绝。
	operations []operationSet
	// jobPathPrefix 是异步任务详情路径前缀（Firecrawl 的 /v2/crawl/{id}）。
	jobPathPrefix string
	// jobStarterPath 是创建异步任务的路径（Firecrawl 的 /v2/crawl）。
	jobStarterPath string
	// authStyle 决定凭据注入方式。
	authStyle authStyle
	// authHeader 是 authAPIKeyHeader 使用的请求头名。
	authHeader string
	// probeURL 是测活端点（含查询串）。
	probeURL string
	// mcpEndpoint 是渠道官方托管 MCP 端点；为空表示该渠道不提供 MCP。
	mcpEndpoint string
	// quotaKinds 是该渠道可能返回的额度维度。
	quotaKinds []QuotaKind
	// quotaCycle 是额度重置周期，用于在无法读到上游重置时间时推算。
	quotaCycle QuotaCycle
	// allowNegativeCredits 表示余额允许为负（Firecrawl 透支时 remainingCredits 为负）。
	allowNegativeCredits bool
}

// channelSpecs 是全部渠道的静态规格表。
var channelSpecs = map[Channel]channelSpec{
	ChannelExa: {
		channel:     ChannelExa,
		displayName: "Exa",
		host:        "https://api.exa.ai",
		operations: []operationSet{
			{"/search", []string{"POST"}},
			{"/contents", []string{"POST"}},
			{"/findSimilar", []string{"POST"}},
		},
		authStyle:  authAPIKeyHeader,
		authHeader: "x-api-key",
		probeURL:   "https://api.exa.ai/agent/runs?limit=1",
		mcpEndpoint: "https://mcp.exa.ai/mcp" +
			"?tools=web_search_exa,web_fetch_exa,web_search_advanced_exa,agent_run",
		quotaKinds: nil,
		quotaCycle: QuotaCycleMonthly,
	}, ChannelAnySearch: {
		channel:     ChannelAnySearch,
		displayName: "AnySearch",
		host:        "https://api.anysearch.com",
		operations: []operationSet{
			{"/v1/sub-domains", []string{"GET"}},
			{"/v1/search", []string{"GET", "POST"}},
			{"/v1/extract", []string{"POST"}},
		},
		authStyle:   authBearer,
		probeURL:    "https://api.anysearch.com/v1/sub-domains?domain=code",
		mcpEndpoint: "https://api.anysearch.com/mcp",
		quotaKinds:  []QuotaKind{QuotaKindPlan},
		quotaCycle:  QuotaCycleDaily,
	},
	ChannelFirecrawl: {
		channel:     ChannelFirecrawl,
		displayName: "Firecrawl",
		host:        "https://api.firecrawl.dev",
		operations: []operationSet{
			{"/v2/scrape", []string{"POST"}},
			{"/v2/search", []string{"POST"}},
			{"/v2/map", []string{"POST"}},
			{"/v2/crawl", []string{"POST"}},
			{"/v2/team/credit-usage", []string{"GET"}},
		},
		jobPathPrefix:        "/v2/crawl/",
		jobStarterPath:       "/v2/crawl",
		authStyle:            authBearer,
		probeURL:             "https://api.firecrawl.dev/v2/team/credit-usage",
		mcpEndpoint:          "https://mcp.firecrawl.dev/v2/mcp",
		quotaKinds:           []QuotaKind{QuotaKindCredits},
		quotaCycle:           QuotaCycleMonthly,
		allowNegativeCredits: true,
	},
	ChannelTavily: {
		channel:     ChannelTavily,
		displayName: "Tavily",
		host:        "https://api.tavily.com",
		operations: []operationSet{
			{"/search", []string{"POST"}},
			{"/extract", []string{"POST"}},
			{"/map", []string{"POST"}},
			{"/crawl", []string{"POST"}},
			{"/usage", []string{"GET"}},
		},
		authStyle:   authBearer,
		probeURL:    "https://api.tavily.com/usage",
		mcpEndpoint: "https://mcp.tavily.com/mcp/",
		quotaKinds:  []QuotaKind{QuotaKindKey, QuotaKindPlan, QuotaKindPaygo},
		quotaCycle:  QuotaCycleMonthly,
	},
	ChannelTinyfish: {
		channel:     ChannelTinyfish,
		displayName: "Tinyfish",
		host:        "https://api.search.tinyfish.ai",
		pathHosts: []pathHost{
			{"/v1/", "https://agent.tinyfish.ai"},
		},
		postHost: "https://api.fetch.tinyfish.ai",
		operations: []operationSet{
			{"/", []string{"GET", "POST"}},
			{"/v1/wallet", []string{"GET"}},
		},
		authStyle:   authAPIKeyHeader,
		authHeader:  "x-api-key",
		probeURL:    "https://agent.tinyfish.ai/v1/wallet",
		mcpEndpoint: "https://agent.tinyfish.ai/mcp",
		quotaKinds:  []QuotaKind{QuotaKindCredits},
		quotaCycle:  QuotaCycleDaily,
	},
}

// Spec 返回渠道静态规格。
func Spec(channel Channel) (channelSpec, bool) {
	spec, ok := channelSpecs[channel]
	return spec, ok
}

// DisplayName 返回渠道展示名。
func (c Channel) DisplayName() string {
	if spec, ok := channelSpecs[c]; ok {
		return spec.displayName
	}
	return string(c)
}

// UpstreamHost 解析某次请求应发往的上游源。
//
// base 非空时直接使用 base（用于自建代理或灰度环境）；否则按渠道规格表选择：
// 先匹配路径前缀覆盖，再匹配方法覆盖，最后回退默认主机。
func (s channelSpec) UpstreamHost(path, method string, base string) string {
	if trimmed := strings.TrimRight(strings.TrimSpace(base), "/"); trimmed != "" {
		return trimmed
	}
	for _, override := range s.pathHosts {
		if strings.HasPrefix(path, override.prefix) {
			return override.host
		}
	}
	if strings.EqualFold(method, "POST") && s.postHost != "" {
		return s.postHost
	}
	return s.host
}

// ValidateOperation 校验路径与方法是否在渠道白名单内。
//
// 除白名单外还会拒绝可疑路径：包含 `\`、`%`、`?`、`#`、NUL 的路径，以及任何
// `.` / `..` 段，避免路径穿越或绕过上游鉴权。
func (s channelSpec) ValidateOperation(path, method string) error {
	if err := validateRawPath(path); err != nil {
		return err
	}
	normalizedMethod := strings.ToUpper(strings.TrimSpace(method))
	for _, operation := range s.operations {
		if operation.path != path {
			continue
		}
		for _, allowed := range operation.methods {
			if normalizedMethod == allowed {
				return nil
			}
		}
		return fmt.Errorf("渠道 %s 不支持 %s %s", s.channel, normalizedMethod, path)
	}
	// 异步任务详情路径按前缀绑定（Firecrawl：GET/DELETE /v2/crawl/{id}）。
	if s.jobPathPrefix != "" && strings.HasPrefix(path, s.jobPathPrefix) {
		if normalizedMethod != "GET" && normalizedMethod != "DELETE" {
			return fmt.Errorf("渠道 %s 的任务详情只支持 GET/DELETE", s.channel)
		}
		if !isValidJobID(strings.TrimPrefix(path, s.jobPathPrefix)) {
			return fmt.Errorf("渠道 %s 的任务 ID 非法", s.channel)
		}
		return nil
	}
	return fmt.Errorf("渠道 %s 不支持路径 %s", s.channel, path)
}

// IsJobStarter 报告该请求是否会创建异步任务（需要把任务绑定到选中的账号）。
func (s channelSpec) IsJobStarter(path, method string) bool {
	return s.jobStarterPath != "" &&
		path == s.jobStarterPath &&
		strings.EqualFold(strings.TrimSpace(method), "POST")
}

// IsJobPath 报告该路径是否是异步任务详情路径。
func (s channelSpec) IsJobPath(path string) bool {
	return s.jobPathPrefix != "" && strings.HasPrefix(path, s.jobPathPrefix)
}

// JobID 从任务详情路径中取出任务 ID；非任务路径返回空串。
func (s channelSpec) JobID(path string) string {
	if !s.IsJobPath(path) {
		return ""
	}
	return strings.TrimPrefix(path, s.jobPathPrefix)
}

// AuthHeader 返回注入上游凭据的请求头名与取值。
func (s channelSpec) AuthHeader(key string) (string, string) {
	if s.authStyle == authAPIKeyHeader {
		return s.authHeader, key
	}
	return "Authorization", "Bearer " + key
}

// ValidateCredentialFreeQuery 校验查询参数中不含客户端凭据字段。
// 客户端凭据只允许通过请求头传递，避免密钥出现在上游日志与 URL 里。
func ValidateCredentialFreeQuery(query map[string][]string) error {
	for name := range query {
		if IsCredentialField(name) {
			return fmt.Errorf("凭据字段 %q 不允许出现在查询参数中，请使用请求头鉴权", name)
		}
	}
	return nil
}

// validateRawPath 拒绝可疑路径字符与点段。
func validateRawPath(path string) error {
	if path == "" {
		return fmt.Errorf("请求路径为空")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("请求路径必须以 / 开头")
	}
	if strings.ContainsAny(path, "\\%?#\x00") {
		return fmt.Errorf("请求路径包含非法字符")
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("请求路径包含点段")
		}
	}
	return nil
}

// isValidJobID 校验异步任务 ID 只含安全字符。
func isValidJobID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// credentialFields 是需要从请求体与查询参数中剥离的凭据字段名。
var credentialFields = map[string]bool{
	"api_key":        true,
	"apikey":         true,
	"api-key":        true,
	"authorization":  true,
	"access_token":   true,
	"x-api-key":      true,
	"tavilyapikey":   true,
	"tavily_api_key": true,
}

// IsCredentialField 报告字段名是否是客户端凭据字段（大小写不敏感）。
func IsCredentialField(name string) bool {
	return credentialFields[strings.ToLower(strings.TrimSpace(name))]
}

// StripCredentialFields 从 JSON 对象中删除客户端凭据字段，返回被删除的字段名。
func StripCredentialFields(payload map[string]any) []string {
	var removed []string
	for name := range payload {
		if IsCredentialField(name) {
			delete(payload, name)
			removed = append(removed, name)
		}
	}
	return removed
}

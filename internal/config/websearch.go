package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xnuyoah/XRouter/v8/internal/constant"
)

// 搜索渠道配置段的默认值。所有默认值都以常量的形式集中在这里，
// 便于面板与文档引用同一份数字。
const (
	// DefaultWebSearchGatewayMaxAttempts 是单次请求最多尝试的账号数。
	DefaultWebSearchGatewayMaxAttempts = 3
	// DefaultWebSearchProbeIntervalSeconds 是渠道状态与额度刷新的调度间隔（秒）。
	DefaultWebSearchProbeIntervalSeconds = 3600
	// DefaultWebSearchProbeConcurrency 是探测任务的并发上限。
	DefaultWebSearchProbeConcurrency = 4
	// DefaultWebSearchProbeTimeoutSeconds 是单次探测请求的超时时间（秒）。
	DefaultWebSearchProbeTimeoutSeconds = 20
	// DefaultWebSearchHistoryDays 是渠道状态历史保留天数。
	DefaultWebSearchHistoryDays = 90
	// DefaultWebSearchQuotaFreshSeconds 是额度快照被视为“新鲜”的时长（秒）。
	DefaultWebSearchQuotaFreshSeconds = 3600
)

// 搜索渠道固定标识列表。渠道集合是编译期固定的，配置只能声明这些渠道。
var webSearchChannelIDs = []string{
	constant.Tavily,
	constant.Exa,
	constant.AnySearch,
	constant.Firecrawl,
	constant.Tinyfish,
}

// WebSearchChannelIDs 返回全部搜索渠道标识的副本，按字母序排列。
func WebSearchChannelIDs() []string {
	out := append([]string(nil), webSearchChannelIDs...)
	sort.Strings(out)
	return out
}

// IsWebSearchChannelID 判断给定标识是否是已知的搜索渠道。
func IsWebSearchChannelID(id string) bool {
	normalized := strings.ToLower(strings.TrimSpace(id))
	for _, candidate := range webSearchChannelIDs {
		if normalized == candidate {
			return true
		}
	}
	return false
}

// WebSearchConfig 定义搜索渠道（tavily / exa / anysearch / firecrawl / tinyfish）
// 的接入配置。它是独立于 LLM 供应商族之外的顶层配置段。
type WebSearchConfig struct {
	// Disabled 关闭整个搜索渠道子系统（包含客户端网关、测活与管理接口）。
	Disabled bool `yaml:"disabled" json:"disabled"`
	// ProxyURL 是探测与测活使用的出站代理。为空时回退到顶层 proxy-url；
	// 两者都为空时探测直接失败，绝不回退直连。
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`
	// Gateway 控制客户端透传网关的行为。
	Gateway WebSearchGatewayConfig `yaml:"gateway" json:"gateway"`
	// Probe 控制测活与渠道状态检测的调度行为。
	Probe WebSearchProbeConfig `yaml:"probe" json:"probe"`
	// Quota 控制额度快照的刷新策略。
	Quota WebSearchQuotaConfig `yaml:"quota" json:"quota"`
	// Channels 声明各渠道及其账号池。
	Channels []WebSearchChannel `yaml:"channels" json:"channels"`
}

// WebSearchGatewayConfig 定义客户端透传网关的行为。
type WebSearchGatewayConfig struct {
	// MaxAttempts 是单次请求最多尝试的账号数；非正数时取默认值。
	MaxAttempts int `yaml:"max-attempts" json:"max-attempts"`
}

// WebSearchProbeConfig 定义测活与渠道状态检测的调度行为。
type WebSearchProbeConfig struct {
	// IntervalSeconds 是定时任务的间隔（秒）；非正数时取默认值。
	IntervalSeconds int `yaml:"interval-seconds" json:"interval-seconds"`
	// Concurrency 是探测并发上限；非正数时取默认值。
	Concurrency int `yaml:"concurrency" json:"concurrency"`
	// TimeoutSeconds 是单次探测请求的超时（秒）；非正数时取默认值。
	TimeoutSeconds int `yaml:"timeout-seconds" json:"timeout-seconds"`
	// HistoryDays 是渠道状态历史保留天数；非正数时取默认值。
	HistoryDays int `yaml:"history-days" json:"history-days"`
}

// WebSearchQuotaConfig 定义额度快照的刷新策略。
type WebSearchQuotaConfig struct {
	// FreshSeconds 是额度快照被认为“新鲜”的时长（秒）；非正数时取默认值。
	FreshSeconds int `yaml:"fresh-seconds" json:"fresh-seconds"`
}

// WebSearchChannel 描述一个搜索渠道及其账号池。
type WebSearchChannel struct {
	// ID 是渠道标识，必须是已知搜索渠道之一。
	ID string `yaml:"id" json:"id"`
	// Name 是展示名称，为空时回退到渠道标识。
	Name string `yaml:"name" json:"name"`
	// Disabled 停用该渠道；停用后客户端请求返回 503，探测也会跳过。
	Disabled bool `yaml:"disabled" json:"disabled"`
	// BaseURL 覆盖默认上游主机（不含路径），用于自建代理或灰度环境。
	BaseURL string `yaml:"base-url" json:"base-url"`
	// ProxyURL 覆盖该渠道的出站代理，为空时继承上面的 ProxyURL。
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`
	// Accounts 是该渠道的上游账号池，每个账号一个 api-key。
	Accounts []WebSearchAccount `yaml:"accounts" json:"accounts"`
}

// WebSearchAccount 是一个上游搜索账号（一个 api-key）。
type WebSearchAccount struct {
	// Name 是账号展示名，为空时由渠道与 api-key 指纹派生。
	Name string `yaml:"name" json:"name"`
	// APIKey 是上游服务商密钥，透传时注入到上游请求头或查询参数。
	APIKey string `yaml:"api-key" json:"api-key"`
	// Username 与 Password 仅 AnySearch 的余额查询使用（需要网页登录才能读到额度）。
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
	// Weight 是账号权重，目前仅用于展示与排序兜底；非正数按 1 处理。
	Weight int `yaml:"weight" json:"weight"`
	// Disabled 停用该账号，停用账号不参与选号与探测。
	Disabled bool `yaml:"disabled" json:"disabled"`
	// Note 是自由备注。
	Note string `yaml:"note" json:"note"`
}

// WebSearchEffectiveEnabled 报告搜索渠道子系统是否启用。
//
// 启用条件是「未显式停用」且「至少声明了一个渠道」：这样没有 web-search 段的既有
// 配置不会因为零值而被动启用，而声明了渠道就等同于开启。
func (cfg *Config) WebSearchEffectiveEnabled() bool {
	if cfg == nil || cfg.WebSearch.Disabled {
		return false
	}
	return len(cfg.WebSearch.Channels) > 0
}

// WebSearchEffectiveProxyURL 返回渠道出站代理：优先渠道级配置，
// 其次配置段级，最后回退到顶层 proxy-url。返回空字符串表示未配置代理。
func (cfg *Config) WebSearchEffectiveProxyURL(channel string) string {
	if cfg == nil {
		return ""
	}
	for _, declared := range cfg.WebSearch.Channels {
		if strings.EqualFold(strings.TrimSpace(declared.ID), strings.TrimSpace(channel)) {
			if trimmed := strings.TrimSpace(declared.ProxyURL); trimmed != "" {
				return trimmed
			}
			break
		}
	}
	if trimmed := strings.TrimSpace(cfg.WebSearch.ProxyURL); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(cfg.ProxyURL)
}

// WebSearchEffectiveMaxAttempts 返回单次请求最多尝试的账号数。
func (cfg *Config) WebSearchEffectiveMaxAttempts() int {
	if cfg != nil && cfg.WebSearch.Gateway.MaxAttempts > 0 {
		return cfg.WebSearch.Gateway.MaxAttempts
	}
	return DefaultWebSearchGatewayMaxAttempts
}

// WebSearchEffectiveProbeIntervalSeconds 返回探测调度间隔（秒）。
func (cfg *Config) WebSearchEffectiveProbeIntervalSeconds() int {
	if cfg != nil && cfg.WebSearch.Probe.IntervalSeconds > 0 {
		return cfg.WebSearch.Probe.IntervalSeconds
	}
	return DefaultWebSearchProbeIntervalSeconds
}

// WebSearchEffectiveProbeConcurrency 返回探测并发上限。
func (cfg *Config) WebSearchEffectiveProbeConcurrency() int {
	if cfg != nil && cfg.WebSearch.Probe.Concurrency > 0 {
		return cfg.WebSearch.Probe.Concurrency
	}
	return DefaultWebSearchProbeConcurrency
}

// WebSearchEffectiveProbeTimeoutSeconds 返回单次探测超时（秒）。
func (cfg *Config) WebSearchEffectiveProbeTimeoutSeconds() int {
	if cfg != nil && cfg.WebSearch.Probe.TimeoutSeconds > 0 {
		return cfg.WebSearch.Probe.TimeoutSeconds
	}
	return DefaultWebSearchProbeTimeoutSeconds
}

// WebSearchEffectiveHistoryDays 返回渠道状态历史保留天数。
func (cfg *Config) WebSearchEffectiveHistoryDays() int {
	if cfg != nil && cfg.WebSearch.Probe.HistoryDays > 0 {
		return cfg.WebSearch.Probe.HistoryDays
	}
	return DefaultWebSearchHistoryDays
}

// WebSearchEffectiveQuotaFreshSeconds 返回额度快照的新鲜时长（秒）。
func (cfg *Config) WebSearchEffectiveQuotaFreshSeconds() int {
	if cfg != nil && cfg.WebSearch.Quota.FreshSeconds > 0 {
		return cfg.WebSearch.Quota.FreshSeconds
	}
	return DefaultWebSearchQuotaFreshSeconds
}

// WebSearchChannelByID 返回指定渠道的配置副本，未声明时返回 false。
func (cfg *Config) WebSearchChannelByID(id string) (WebSearchChannel, bool) {
	if cfg == nil {
		return WebSearchChannel{}, false
	}
	for _, declared := range cfg.WebSearch.Channels {
		if strings.EqualFold(strings.TrimSpace(declared.ID), strings.TrimSpace(id)) {
			return declared, true
		}
	}
	return WebSearchChannel{}, false
}

// ValidateWebSearchConfig 校验搜索渠道配置段。它只做结构性校验（渠道标识、重复声明、
// 空密钥），不发起任何网络请求。
func ValidateWebSearchConfig(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(cfg.WebSearch.Channels))
	for i, channel := range cfg.WebSearch.Channels {
		id := strings.ToLower(strings.TrimSpace(channel.ID))
		if !IsWebSearchChannelID(id) {
			return fmt.Errorf("web-search.channels[%d]: 未知渠道 %q（可选：%s）",
				i, channel.ID, strings.Join(WebSearchChannelIDs(), ", "))
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("web-search.channels[%d]: 渠道 %q 重复声明", i, id)
		}
		seen[id] = struct{}{}
		for j, account := range channel.Accounts {
			if strings.TrimSpace(account.APIKey) == "" {
				return fmt.Errorf("web-search.channels[%d].accounts[%d]: api-key 不能为空", i, j)
			}
		}
	}
	return nil
}

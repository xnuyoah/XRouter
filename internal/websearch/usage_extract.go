package websearch

import (
	"math"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// UsageExtraction is per-response billing metadata reported by an upstream.
type UsageExtraction struct {
	Credits          float64
	UpstreamCost     *float64
	UpstreamCurrency string
}

// ExtractResponseUsage reads known per-request credit/cost fields from a
// successful upstream response. Unknown channels or fields return zero values.
func ExtractResponseUsage(channel Channel, operation string, statusCode int, _ http.Header, body []byte) UsageExtraction {
	if statusCode < 200 || statusCode >= 300 || len(body) == 0 {
		return UsageExtraction{}
	}
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "usage", "credit_usage", "wallet", "agent_runs", "sub_domains":
		// Metadata/balance endpoints do not represent billable search usage.
		return UsageExtraction{}
	}
	root := gjson.ParseBytes(body)
	switch channel {
	case ChannelTavily:
		if credits := positiveNumber(root.Get("usage.credits")); credits > 0 {
			return UsageExtraction{Credits: credits}
		}
		if credits := positiveNumber(root.Get("search_cost")); credits > 0 {
			return UsageExtraction{Credits: credits}
		}
		if credits := positiveNumber(root.Get("cost")); credits > 0 {
			return UsageExtraction{Credits: credits}
		}
	case ChannelFirecrawl:
		// A crawl status response reports cumulative crawl credits. Recording it
		// again on every status poll would double count the crawl, so only
		// extract from direct operations.
		if operation == "crawl_status" {
			return UsageExtraction{}
		}
		if credits := positiveNumber(root.Get("creditsUsed")); credits > 0 {
			return UsageExtraction{Credits: credits}
		}
		if credits := positiveNumber(root.Get("credits_used")); credits > 0 {
			return UsageExtraction{Credits: credits}
		}
	case ChannelExa:
		if cost := nonNegativeNumber(root.Get("costDollars.total")); cost > 0 {
			return UsageExtraction{UpstreamCost: &cost, UpstreamCurrency: "USD"}
		}
	case ChannelAnySearch:
		if credits := positiveNumber(root.Get("usage.credits")); credits > 0 {
			return UsageExtraction{Credits: credits}
		}
		if credits := positiveNumber(root.Get("credits")); credits > 0 {
			return UsageExtraction{Credits: credits}
		}
	case ChannelTinyfish:
		// Search and fetch are free; agent/browser responses may still report
		// credits in usage.credits on some plans.
		if credits := positiveNumber(root.Get("usage.credits")); credits > 0 {
			return UsageExtraction{Credits: credits}
		}
		if credits := positiveNumber(root.Get("creditsUsed")); credits > 0 {
			return UsageExtraction{Credits: credits}
		}
	}
	return UsageExtraction{}
}

func positiveNumber(result gjson.Result) float64 {
	value := nonNegativeNumber(result)
	if value <= 0 {
		return 0
	}
	return value
}

func nonNegativeNumber(result gjson.Result) float64 {
	if !result.Exists() {
		return 0
	}
	value := result.Float()
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return value
}

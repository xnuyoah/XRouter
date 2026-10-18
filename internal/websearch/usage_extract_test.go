package websearch

import (
	"math"
	"net/http"
	"testing"
)

func TestExtractResponseUsage(t *testing.T) {
	cases := []struct {
		name      string
		channel   Channel
		operation string
		status    int
		body      string
		credits   float64
		cost      float64
		currency  string
	}{
		{
			name:    "tavily usage credits",
			channel: ChannelTavily, operation: "search", status: 200,
			body: `{"usage":{"credits":2}}`, credits: 2,
		},
		{
			name:    "firecrawl credits used",
			channel: ChannelFirecrawl, operation: "scrape", status: 200,
			body: `{"creditsUsed":3}`, credits: 3,
		},
		{
			name:    "firecrawl crawl status is skipped",
			channel: ChannelFirecrawl, operation: "crawl_status", status: 200,
			body: `{"creditsUsed":10}`,
		},
		{
			name:    "exa dollar cost",
			channel: ChannelExa, operation: "search", status: 200,
			body: `{"costDollars":{"total":0.007}}`, cost: 0.007, currency: "USD",
		},
		{
			name:    "non successful response is ignored",
			channel: ChannelTavily, operation: "search", status: 500,
			body: `{"usage":{"credits":2}}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ExtractResponseUsage(testCase.channel, testCase.operation, testCase.status, http.Header{}, []byte(testCase.body))
			if math.Abs(got.Credits-testCase.credits) > 1e-9 {
				t.Fatalf("credits = %v, want %v", got.Credits, testCase.credits)
			}
			if testCase.cost == 0 {
				if got.UpstreamCost != nil {
					t.Fatalf("cost = %v, want nil", *got.UpstreamCost)
				}
				return
			}
			if got.UpstreamCost == nil || math.Abs(*got.UpstreamCost-testCase.cost) > 1e-9 {
				t.Fatalf("cost = %v, want %v", got.UpstreamCost, testCase.cost)
			}
			if got.UpstreamCurrency != testCase.currency {
				t.Fatalf("currency = %q, want %q", got.UpstreamCurrency, testCase.currency)
			}
		})
	}
}

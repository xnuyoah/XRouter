package websearch

import (
	"strings"
	"testing"
)

func TestParseChannel(t *testing.T) {
	for _, raw := range []string{"tavily", "TAVILY", " Tavily ", "exa", "anysearch", "firecrawl", "tinyfish"} {
		if _, errParse := ParseChannel(raw); errParse != nil {
			t.Fatalf("ParseChannel(%q) 失败: %v", raw, errParse)
		}
	}
	if _, errParse := ParseChannel("brave"); errParse == nil {
		t.Fatal("未知渠道应当返回错误")
	}
	if _, errParse := ParseChannel(""); errParse == nil {
		t.Fatal("空渠道应当返回错误")
	}
}

func TestAllChannelsCoverEverySpec(t *testing.T) {
	channels := AllChannels()
	if len(channels) != 5 {
		t.Fatalf("渠道数量 = %d，期望 5", len(channels))
	}
	for _, channel := range channels {
		spec, ok := Spec(channel)
		if !ok {
			t.Fatalf("渠道 %s 缺少规格", channel)
		}
		if spec.host == "" || spec.probeURL == "" {
			t.Fatalf("渠道 %s 规格不完整: %+v", channel, spec)
		}
		if len(spec.operations) == 0 {
			t.Fatalf("渠道 %s 缺少操作白名单", channel)
		}
	}
}

func TestUpstreamHostResolution(t *testing.T) {
	tinyfish, _ := Spec(ChannelTinyfish)
	for _, tc := range []struct {
		name   string
		path   string
		method string
		base   string
		want   string
	}{
		{"tinyfish 搜索", "/", "GET", "", "https://api.search.tinyfish.ai"},
		{"tinyfish 抓取", "/", "POST", "", "https://api.fetch.tinyfish.ai"},
		{"tinyfish 钱包", "/v1/wallet", "GET", "", "https://agent.tinyfish.ai"},
		{"配置覆盖优先", "/", "GET", "https://staging.example.com/", "https://staging.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tinyfish.UpstreamHost(tc.path, tc.method, tc.base); got != tc.want {
				t.Fatalf("UpstreamHost = %q，期望 %q", got, tc.want)
			}
		})
	}

	exa, _ := Spec(ChannelExa)
	if got := exa.UpstreamHost("/search", "POST", ""); got != "https://api.exa.ai" {
		t.Fatalf("exa 主机 = %q", got)
	}
}

func TestValidateOperation(t *testing.T) {
	exa, _ := Spec(ChannelExa)
	if errValidate := exa.ValidateOperation("/search", "POST"); errValidate != nil {
		t.Fatalf("exa /search POST 应当允许: %v", errValidate)
	}
	if errValidate := exa.ValidateOperation("/search", "GET"); errValidate == nil {
		t.Fatal("exa /search GET 应当被拒绝")
	}
	if errValidate := exa.ValidateOperation("/admin", "POST"); errValidate == nil {
		t.Fatal("exa 未声明的路径应当被拒绝")
	}

	tinyfish, _ := Spec(ChannelTinyfish)
	if errValidate := tinyfish.ValidateOperation("/", "GET"); errValidate != nil {
		t.Fatalf("tinyfish 根路径 GET 应当允许: %v", errValidate)
	}
	if errValidate := tinyfish.ValidateOperation("/", "DELETE"); errValidate == nil {
		t.Fatal("tinyfish 根路径 DELETE 应当被拒绝")
	}

	anysearch, _ := Spec(ChannelAnySearch)
	if errValidate := anysearch.ValidateOperation("/v1/search", "GET"); errValidate != nil {
		t.Fatalf("anysearch /v1/search GET 应当允许: %v", errValidate)
	}
	if errValidate := anysearch.ValidateOperation("/v1/extract", "GET"); errValidate == nil {
		t.Fatal("anysearch /v1/extract GET 应当被拒绝")
	}
}

func TestValidateOperationRejectsSuspiciousPaths(t *testing.T) {
	tavily, _ := Spec(ChannelTavily)
	for _, path := range []string{"/search/../admin", "/search%2fadmin", "/search?x=1", "/search#frag", "/search\\x", ""} {
		if errValidate := tavily.ValidateOperation(path, "POST"); errValidate == nil {
			t.Fatalf("路径 %q 应当被拒绝", path)
		}
	}
}

func TestFirecrawlJobPaths(t *testing.T) {
	firecrawl, _ := Spec(ChannelFirecrawl)
	if !firecrawl.IsJobStarter("/v2/crawl", "POST") {
		t.Fatal("/v2/crawl POST 应当是任务创建请求")
	}
	if firecrawl.IsJobStarter("/v2/crawl", "GET") {
		t.Fatal("/v2/crawl GET 不是任务创建请求")
	}
	if !firecrawl.IsJobPath("/v2/crawl/abc-123") {
		t.Fatal("/v2/crawl/{id} 应当是任务详情路径")
	}
	if got := firecrawl.JobID("/v2/crawl/abc-123"); got != "abc-123" {
		t.Fatalf("JobID = %q", got)
	}
	if errValidate := firecrawl.ValidateOperation("/v2/crawl/abc-123", "GET"); errValidate != nil {
		t.Fatalf("任务详情 GET 应当允许: %v", errValidate)
	}
	if errValidate := firecrawl.ValidateOperation("/v2/crawl/abc-123", "POST"); errValidate == nil {
		t.Fatal("任务详情 POST 应当被拒绝")
	}
	if errValidate := firecrawl.ValidateOperation("/v2/crawl/bad!id", "GET"); errValidate == nil {
		t.Fatal("非法任务 ID 应当被拒绝")
	}
}

func TestAuthHeaderStyle(t *testing.T) {
	for _, channel := range []Channel{ChannelExa, ChannelTinyfish} {
		spec, _ := Spec(channel)
		name, value := spec.AuthHeader("secret")
		if name != "x-api-key" || value != "secret" {
			t.Fatalf("渠道 %s 鉴权头 = %q:%q，期望 x-api-key:secret", channel, name, value)
		}
	}
	for _, channel := range []Channel{ChannelTavily, ChannelFirecrawl, ChannelAnySearch} {
		spec, _ := Spec(channel)
		name, value := spec.AuthHeader("secret")
		if name != "Authorization" || value != "Bearer secret" {
			t.Fatalf("渠道 %s 鉴权头 = %q:%q，期望 Authorization:Bearer secret", channel, name, value)
		}
	}
}

func TestCredentialFieldsAndStripping(t *testing.T) {
	for _, name := range []string{"api_key", "API_KEY", "api-key", "Authorization", "access_token", "tavilyApiKey"} {
		if !IsCredentialField(name) {
			t.Fatalf("字段 %q 应当被识别为凭据", name)
		}
	}
	if IsCredentialField("query") {
		t.Fatal("query 不应被识别为凭据")
	}

	payload := map[string]any{"query": "golang", "api_key": "leak", "Authorization": "Bearer leak"}
	removed := StripCredentialFields(payload)
	if len(removed) != 2 {
		t.Fatalf("剥离字段数 = %d，期望 2", len(removed))
	}
	if _, exists := payload["api_key"]; exists {
		t.Fatal("api_key 未被剥离")
	}
	if payload["query"] != "golang" {
		t.Fatal("业务字段被误删")
	}
}

func TestValidateCredentialFreeQuery(t *testing.T) {
	if errQuery := ValidateCredentialFreeQuery(map[string][]string{"query": {"x"}}); errQuery != nil {
		t.Fatalf("普通查询参数应当允许: %v", errQuery)
	}
	errQuery := ValidateCredentialFreeQuery(map[string][]string{"api_key": {"x"}})
	if errQuery == nil || !strings.Contains(errQuery.Error(), "api_key") {
		t.Fatalf("凭据查询参数应当被拒绝，实际: %v", errQuery)
	}
}

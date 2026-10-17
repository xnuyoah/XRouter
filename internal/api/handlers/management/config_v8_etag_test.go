package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/config"
)

// TestConfigV8ETagIfMatch 覆盖 issue #6666：v8 配置端点必须支持
// 乐观并发，以免一次陈旧的整文档写入复活另一个写入者
// 已经保存的更改（例如一个已被吊销的 API key）。
func TestConfigV8ETagIfMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "config-version: 8\nserver: {port: 11016}\naccess: {api-keys: [revoked-key]}\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		router.Handle(method, "/v8/management/config", h.ConfigV8)
		router.Handle(method, "/v8/management/config.yaml", h.ConfigV8)
		router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
	}

	do := func(method, url, body, ifMatch string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		router.ServeHTTP(recorder, req)
		return recorder
	}
	wantStatus := func(recorder *httptest.ResponseRecorder, status int) {
		t.Helper()
		if recorder.Code != status {
			t.Fatalf("status=%d want %d body=%s", recorder.Code, status, recorder.Body.String())
		}
	}
	fileBytes := func() []byte {
		t.Helper()
		data, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		return data
	}
	fileETag := func() string { return `"` + config.ComputeSourceHash(fileBytes()) + `"` }

	// 在根路径使用陈旧 ETag 的 PATCH 会快速失败并返回 412，因此 PATCH 受到
	// 相同前置条件的约束，而不是盲目地合并。
	recorder := do(http.MethodPatch, "/v8/management/config", `{"server":{"port":8319}}`, `"stale"`)
	wantStatus(recorder, http.StatusPreconditionFailed)
	if string(fileBytes()) != raw {
		t.Fatal("stale PATCH changed the file")
	}
	// 使用当前 ETag 的 PATCH 会通过并返回新的 ETag。
	recorder = do(http.MethodPatch, "/v8/management/config", `{"server":{"port":8319}}`, fileETag())
	wantStatus(recorder, http.StatusOK)
	if got := recorder.Header().Get("ETag"); got != fileETag() {
		t.Fatalf("PATCH response ETag=%q want %q", got, fileETag())
	}
	loaded, err := config.LoadConfig(path)
	if err != nil || loaded.Port != 8319 {
		t.Fatalf("PATCH with matching If-Match not applied: port=%d err=%v", loaded.Port, err)
	}

	// GET 响应将以磁盘上的版本作为强 ETag 进行通告。
	get := do(http.MethodGet, "/v8/management/config.yaml", "", "")
	wantStatus(get, http.StatusOK)
	etag := get.Header().Get("ETag")
	if etag != fileETag() {
		t.Fatalf("GET config.yaml ETag=%q want %q", etag, fileETag())
	}
	subPathGet := do(http.MethodGet, "/v8/management/config/server", "", "")
	wantStatus(subPathGet, http.StatusOK)
	if got := subPathGet.Header().Get("ETag"); got != etag {
		t.Fatalf("GET sub-path ETag=%q want %q", got, etag)
	}

	// 另一个写入者在上述读取与我们的写入之间吊销了该 API key。
	revoked := do(http.MethodPut, "/v8/management/config/access/api-keys", `["other-key"]`, "")
	wantStatus(revoked, http.StatusOK)
	if got := revoked.Header().Get("ETag"); got != fileETag() {
		t.Fatalf("PUT response ETag=%q want %q", got, fileETag())
	}

	// 基于陈旧读取的整文档 PUT 不得复活该 key。
	before := string(fileBytes())
	stale := do(http.MethodPut, "/v8/management/config.yaml", raw, etag)
	wantStatus(stale, http.StatusPreconditionFailed)
	if got := stale.Header().Get("ETag"); got != fileETag() {
		t.Fatalf("412 response ETag=%q want current %q", got, fileETag())
	}
	if string(fileBytes()) != before {
		t.Fatal("stale PUT changed the file")
	}
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.APIKeys) != 1 || loaded.APIKeys[0] != "other-key" {
		t.Fatalf("stale PUT resurrected revoked key: %v", loaded.APIKeys)
	}

	// 使用陈旧 ETag 的 DELETE 会以相同方式被拒绝。
	staleDelete := do(http.MethodDelete, "/v8/management/config/access/api-keys", "", etag)
	wantStatus(staleDelete, http.StatusPreconditionFailed)
	if string(fileBytes()) != before {
		t.Fatal("stale DELETE changed the file")
	}

	// 弱校验器和格式错误的校验器在强比较下永不匹配。引号内的
	// 逗号是标签内容，而非列表分隔符：无论是带引号还是弱引号的
	// "stale,*,tag" 都不得被误认为通配符，且只有整个字段
	// 可以是通配符。
	for _, ifMatch := range []string{"W/" + fileETag(), "not-an-etag", `"unterminated`, `"a"b"`, `"stale,*,tag"`, `W/"stale,*,tag"`, `"stale", *`} {
		recorder = do(http.MethodPut, "/v8/management/config.yaml", before, ifMatch)
		wantStatus(recorder, http.StatusPreconditionFailed)
	}

	// 分散在多个重复报头行中的实体标签属于同一个列表。
	recorder = httptest.NewRecorder()
	splitReq := httptest.NewRequest(http.MethodPut, "/v8/management/config/server/port", strings.NewReader(`8318`))
	splitReq.Header.Add("If-Match", `"stale"`)
	splitReq.Header.Add("If-Match", fileETag())
	router.ServeHTTP(recorder, splitReq)
	wantStatus(recorder, http.StatusOK)

	// 引号内带有尾随逗号的当前哈希是一个不同的（陈旧的）
	// 标签，而非当前标签。
	quotedComma := `"` + config.ComputeSourceHash(fileBytes()) + `,"`
	recorder = do(http.MethodPut, "/v8/management/config/server/port", `11016`, quotedComma)
	wantStatus(recorder, http.StatusPreconditionFailed)

	// 显式存在但为空/空白的 If-Match 不携带任何标签并会失败；
	// 只有完全缺失该报头时才保持无条件。（do 辅助函数无法
	// 表示空报头，因此直接添加它。）
	for _, blank := range []string{"", "  \t  "} {
		recorder = httptest.NewRecorder()
		blankReq := httptest.NewRequest(http.MethodPut, "/v8/management/config/server/port", strings.NewReader(`11016`))
		blankReq.Header.Add("If-Match", blank)
		router.ServeHTTP(recorder, blankReq)
		wantStatus(recorder, http.StatusPreconditionFailed)
	}

	// 包含当前标签（带有空白和弱成员）的列表会通过。
	current := fileETag()
	listed := do(http.MethodDelete, "/v8/management/config/access/api-keys", "", `"stale", `+current+` , W/`+current)
	wantStatus(listed, http.StatusOK)
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.APIKeys) != 0 {
		t.Fatalf("DELETE with matching If-Match list not applied: %v", loaded.APIKeys)
	}

	// 星号匹配任何已存在的版本。
	star := do(http.MethodPut, "/v8/management/config/server/port", `8320`, "*")
	wantStatus(star, http.StatusOK)

	// 匹配的 ETag 可以链式使用：复用 PUT 响应的 ETag，无需重新读取。
	first := do(http.MethodPut, "/v8/management/config/server/port", `8321`, fileETag())
	wantStatus(first, http.StatusOK)
	nextETag := first.Header().Get("ETag")
	if nextETag == "" || nextETag != fileETag() {
		t.Fatalf("PUT response ETag=%q want on-disk hash %q", nextETag, fileETag())
	}
	second := do(http.MethodPut, "/v8/management/config/server/port", `8322`, nextETag)
	wantStatus(second, http.StatusOK)

	// 不带 If-Match 时行为保持不变。
	unconditional := do(http.MethodPut, "/v8/management/config/server/port", `8323`, "")
	wantStatus(unconditional, http.StatusOK)

	// GET 404 响应不通告 ETag。
	missing := do(http.MethodGet, "/v8/management/config/no-such-key", "", "")
	wantStatus(missing, http.StatusNotFound)
	if got := missing.Header().Get("ETag"); got != "" {
		t.Fatalf("404 ETag=%q want empty", got)
	}
}

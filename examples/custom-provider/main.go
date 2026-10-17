// Package main 演示如何创建自定义 AI 供应商执行器
// 并将其集成到 XRouter 服务器中。本示例展示如何：
// - 创建一个实现 Executor 接口的自定义执行器
// - 注册用于请求/响应转换的自定义转换器
// - 将自定义供应商与 SDK 服务器集成
// - 在模型注册表中注册自定义模型
//
// 本示例使用一个简单的回声服务（httpbin.org）作为上游 API
// 用于演示目的。在真实实现中，你应当将其替换为
// 你实际的 AI 服务供应商。
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/sdk/api"
	sdkAuth "github.com/xnuyoah/XRouter/v8/sdk/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	clipexec "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
	"github.com/xnuyoah/XRouter/v8/sdk/config"
	"github.com/xnuyoah/XRouter/v8/sdk/logging"
	sdktr "github.com/xnuyoah/XRouter/v8/sdk/translator"
)

const (
	// providerKey 是我们自定义供应商的标识符。
	providerKey = "myprov"

	// fOpenAI 表示 OpenAI chat 格式。
	fOpenAI = sdktr.Format("openai.chat")

	// fMyProv 表示我们自定义供应商的 chat 格式。
	fMyProv = sdktr.Format("myprov.chat")
)

// init 为演示目的注册简单的转换器。
// 在真实实现中，你应当实现 OpenAI 格式与你的供应商格式之间
// 恰当的请求/响应转换逻辑。
func init() {
	sdktr.Register(fOpenAI, fMyProv,
		func(model string, raw []byte, stream bool) ([]byte, error) { return raw, nil },
		sdktr.ResponseTransform{
			Stream: func(ctx context.Context, model string, originalReq, translatedReq, raw []byte, param *any) [][]byte {
				return [][]byte{raw}
			},
			NonStream: func(ctx context.Context, model string, originalReq, translatedReq, raw []byte, param *any) []byte {
				return raw
			},
		},
	)
}

// MyExecutor 是一个用于演示目的的最小供应商实现。
// 它实现 Executor 接口，以处理发往自定义 AI 供应商的请求。
type MyExecutor struct{}

// Identifier 返回此执行器的唯一标识符。
func (MyExecutor) Identifier() string { return providerKey }

// PrepareRequest 可选地向原始 HTTP 请求注入凭据。
// 该方法在每个请求之前被调用，使执行器可以用认证头
// 或其他必要的修改来调整 HTTP 请求。
//
// 参数：
//   - req: 要准备的 HTTP 请求
//   - a: 认证信息
//
// 返回：
//   - error: 请求准备失败时的错误
func (MyExecutor) PrepareRequest(req *http.Request, a *coreauth.Auth) error {
	if req == nil || a == nil {
		return nil
	}
	if a.Attributes != nil {
		if ak := strings.TrimSpace(a.Attributes["api_key"]); ak != "" {
			req.Header.Set("Authorization", "Bearer "+ak)
		}
	}
	return nil
}

func buildHTTPClient(a *coreauth.Auth) *http.Client {
	if a == nil || strings.TrimSpace(a.ProxyURL) == "" {
		return http.DefaultClient
	}
	u, err := url.Parse(a.ProxyURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return http.DefaultClient
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
}

func upstreamEndpoint(a *coreauth.Auth) string {
	if a != nil && a.Attributes != nil {
		if ep := strings.TrimSpace(a.Attributes["endpoint"]); ep != "" {
			return ep
		}
	}
	// 演示用回声端点；请替换为你的上游。
	return "https://httpbin.org/post"
}

func (MyExecutor) Execute(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	client := buildHTTPClient(a)
	endpoint := upstreamEndpoint(a)

	httpReq, errNew := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(req.Payload))
	if errNew != nil {
		return clipexec.Response{}, errNew
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// 通过 PrepareRequest 钩子注入凭据。
	if errPrep := (MyExecutor{}).PrepareRequest(httpReq, a); errPrep != nil {
		return clipexec.Response{}, errPrep
	}

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return clipexec.Response{}, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			fmt.Fprintf(os.Stderr, "close response body error: %v\n", errClose)
		}
	}()
	body, _ := io.ReadAll(resp.Body)
	return clipexec.Response{Payload: body}, nil
}

func (MyExecutor) HttpRequest(ctx context.Context, a *coreauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("myprov executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrep := (MyExecutor{}).PrepareRequest(httpReq, a); errPrep != nil {
		return nil, errPrep
	}
	client := buildHTTPClient(a)
	return client.Do(httpReq)
}

func (MyExecutor) CountTokens(context.Context, *coreauth.Auth, clipexec.Request, clipexec.Options) (clipexec.Response, error) {
	return clipexec.Response{}, errors.New("count tokens not implemented")
}

func (MyExecutor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (*clipexec.StreamResult, error) {
	ch := make(chan clipexec.StreamChunk, 1)
	go func() {
		defer close(ch)
		ch <- clipexec.StreamChunk{Payload: []byte("data: {\"ok\":true}\n\n")}
	}()
	return &clipexec.StreamResult{Chunks: ch}, nil
}

func (MyExecutor) Refresh(ctx context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}

func main() {
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		panic(err)
	}

	tokenStore := sdkAuth.GetTokenStore()
	if dirSetter, ok := tokenStore.(interface{ SetBaseDir(string) }); ok {
		dirSetter.SetBaseDir(cfg.AuthDir)
	}
	core := coreauth.NewManager(tokenStore, nil, nil)
	core.RegisterExecutor(MyExecutor{})

	hooks := cliproxy.Hooks{
		OnAfterStart: func(s *cliproxy.Service) {
			// 为自定义供应商注册演示模型，使它们出现在 /v1/models 中。
			models := []*cliproxy.ModelInfo{{ID: "myprov-pro-1", Object: "model", Type: providerKey, DisplayName: "MyProv Pro 1"}}
			for _, a := range core.List() {
				if strings.EqualFold(a.Provider, providerKey) {
					cliproxy.GlobalModelRegistry().RegisterClient(a.ID, providerKey, models)
				}
			}
		},
	}

	svc, err := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath("config.yaml").
		WithCoreAuthManager(core).
		WithServerOptions(
			// 可选：添加一个简单的中间件 + 自定义请求日志记录器
			api.WithMiddleware(func(c *gin.Context) { c.Header("X-Example", "custom-provider"); c.Next() }),
			api.WithRequestLoggerFactory(func(cfg *config.Config, cfgPath string) logging.RequestLogger {
				return logging.NewFileRequestLoggerWithOptions(true, "logs", filepath.Dir(cfgPath), cfg.ErrorLogsMaxFiles)
			}),
		).
		WithHooks(hooks).
		Build()
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if errRun := svc.Run(ctx); errRun != nil && !errors.Is(errRun, context.Canceled) {
		panic(errRun)
	}
	_ = os.Stderr // 保持 os 导入被使用（仅演示用）
	_ = time.Second
}

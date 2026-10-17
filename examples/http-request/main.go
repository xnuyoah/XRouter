// Package main 演示如何使用 coreauth.Manager.HttpRequest/NewHttpRequest
// 在注入供应商凭据的情况下执行任意 HTTP 请求。
//
// 本示例注册一个最小的自定义执行器，它从 auth.Attributes["api_key"]
// 注入 Authorization 头，然后对 httpbin.org 发起两个请求
// 以展示注入的头。
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	coreauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	clipexec "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

const providerKey = "echo"

// EchoExecutor 是一个用于演示目的的最小供应商实现。
type EchoExecutor struct{}

func (EchoExecutor) Identifier() string { return providerKey }

func (EchoExecutor) PrepareRequest(req *http.Request, auth *coreauth.Auth) error {
	if req == nil || auth == nil {
		return nil
	}
	if auth.Attributes != nil {
		if apiKey := strings.TrimSpace(auth.Attributes["api_key"]); apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
	return nil
}

func (EchoExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("echo executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrep := (EchoExecutor{}).PrepareRequest(httpReq, auth); errPrep != nil {
		return nil, errPrep
	}
	return http.DefaultClient.Do(httpReq)
}

func (EchoExecutor) Execute(context.Context, *coreauth.Auth, clipexec.Request, clipexec.Options) (clipexec.Response, error) {
	return clipexec.Response{}, errors.New("echo executor: Execute not implemented")
}

func (EchoExecutor) ExecuteStream(context.Context, *coreauth.Auth, clipexec.Request, clipexec.Options) (*clipexec.StreamResult, error) {
	return nil, errors.New("echo executor: ExecuteStream not implemented")
}

func (EchoExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, errors.New("echo executor: Refresh not implemented")
}

func (EchoExecutor) CountTokens(context.Context, *coreauth.Auth, clipexec.Request, clipexec.Options) (clipexec.Response, error) {
	return clipexec.Response{}, errors.New("echo executor: CountTokens not implemented")
}

func main() {
	log.SetLevel(log.InfoLevel)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	core := coreauth.NewManager(nil, nil, nil)
	core.RegisterExecutor(EchoExecutor{})

	auth := &coreauth.Auth{
		ID:       "demo-echo",
		Provider: providerKey,
		Attributes: map[string]string{
			"api_key": "demo-api-key",
		},
	}

	// 示例 1：构建一个准备好的请求，并使用你自己的 http.Client 执行它。
	reqPrepared, errReqPrepared := core.NewHttpRequest(
		ctx,
		auth,
		http.MethodGet,
		"https://httpbin.org/anything",
		nil,
		http.Header{"X-Example": []string{"prepared"}},
	)
	if errReqPrepared != nil {
		panic(errReqPrepared)
	}
	respPrepared, errDoPrepared := http.DefaultClient.Do(reqPrepared)
	if errDoPrepared != nil {
		panic(errDoPrepared)
	}
	defer func() {
		if errClose := respPrepared.Body.Close(); errClose != nil {
			log.Errorf("close response body error: %v", errClose)
		}
	}()
	bodyPrepared, errReadPrepared := io.ReadAll(respPrepared.Body)
	if errReadPrepared != nil {
		panic(errReadPrepared)
	}
	fmt.Printf("Prepared request status: %d\n%s\n\n", respPrepared.StatusCode, bodyPrepared)

	// 示例 2：通过 core.HttpRequest 执行原始请求（自动注入 + 发起）。
	rawBody := []byte(`{"hello":"world"}`)
	rawReq, errRawReq := http.NewRequestWithContext(ctx, http.MethodPost, "https://httpbin.org/anything", bytes.NewReader(rawBody))
	if errRawReq != nil {
		panic(errRawReq)
	}
	rawReq.Header.Set("Content-Type", "application/json")
	rawReq.Header.Set("X-Example", "executed")

	respExec, errDoExec := core.HttpRequest(ctx, auth, rawReq)
	if errDoExec != nil {
		panic(errDoExec)
	}
	defer func() {
		if errClose := respExec.Body.Close(); errClose != nil {
			log.Errorf("close response body error: %v", errClose)
		}
	}()
	bodyExec, errReadExec := io.ReadAll(respExec.Body)
	if errReadExec != nil {
		panic(errReadExec)
	}
	fmt.Printf("Manager HttpRequest status: %d\n%s\n", respExec.StatusCode, bodyExec)
}

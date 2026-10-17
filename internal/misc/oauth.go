package misc

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// GenerateRandomState 生成密码学安全的随机 state 参数，
// 用于 OAuth2 流程以防止 CSRF 攻击。
//
// 返回：
//   - string: 十六进制编码的随机 state 字符串
//   - error: 随机生成失败时返回错误，否则为 nil
func GenerateRandomState() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// OAuthCallback 保存解析后的 OAuth 回调参数。
type OAuthCallback struct {
	Code             string
	State            string
	ClientID         string
	Error            string
	ErrorDescription string
}

// AsyncPrompt 在 goroutine 中运行提示函数，并返回用于获取
// 结果的 channel。返回的 channel 带缓冲（大小 1），因此即使调用方
// 放弃这些 channel，goroutine 仍能完成。
func AsyncPrompt(promptFn func(string) (string, error), message string) (<-chan string, <-chan error) {
	inputCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		input, err := promptFn(message)
		if err != nil {
			errCh <- err
			return
		}
		inputCh <- input
	}()
	return inputCh, errCh
}

// ParseOAuthCallback 从回调 URL 中提取 OAuth 参数。
// 输入为空时返回 nil。
func ParseOAuthCallback(input string) (*OAuthCallback, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, nil
	}

	candidate := trimmed
	if !strings.Contains(candidate, "://") {
		if strings.HasPrefix(candidate, "?") {
			candidate = "http://localhost" + candidate
		} else if strings.ContainsAny(candidate, "/?#") || strings.Contains(candidate, ":") {
			candidate = "http://" + candidate
		} else if strings.Contains(candidate, "=") {
			candidate = "http://localhost/?" + candidate
		} else {
			return nil, fmt.Errorf("invalid callback URL")
		}
	}

	parsedURL, err := url.Parse(candidate)
	if err != nil {
		return nil, err
	}

	query := parsedURL.Query()
	code := strings.TrimSpace(query.Get("code"))
	state := strings.TrimSpace(query.Get("state"))
	clientID := strings.TrimSpace(query.Get("client_id"))
	errCode := strings.TrimSpace(query.Get("error"))
	errDesc := strings.TrimSpace(query.Get("error_description"))

	if parsedURL.Fragment != "" {
		if fragQuery, errFrag := url.ParseQuery(parsedURL.Fragment); errFrag == nil {
			if code == "" {
				code = strings.TrimSpace(fragQuery.Get("code"))
			}
			if state == "" {
				state = strings.TrimSpace(fragQuery.Get("state"))
			}
			if clientID == "" {
				clientID = strings.TrimSpace(fragQuery.Get("client_id"))
			}
			if errCode == "" {
				errCode = strings.TrimSpace(fragQuery.Get("error"))
			}
			if errDesc == "" {
				errDesc = strings.TrimSpace(fragQuery.Get("error_description"))
			}
		}
	}

	if code != "" && state == "" && strings.Contains(code, "#") {
		parts := strings.SplitN(code, "#", 2)
		code = parts[0]
		state = parts[1]
	}

	if errCode == "" && errDesc != "" {
		errCode = errDesc
		errDesc = ""
	}

	if code == "" && errCode == "" {
		return nil, fmt.Errorf("callback URL missing code")
	}

	return &OAuthCallback{
		Code:             code,
		State:            state,
		ClientID:         clientID,
		Error:            errCode,
		ErrorDescription: errDesc,
	}, nil
}

package helps

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	log "github.com/sirupsen/logrus"
	internalcache "github.com/xnuyoah/XRouter/v8/internal/cache"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/httpwire"
	cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	"github.com/xnuyoah/XRouter/v8/sdk/httptransport"
	"github.com/xnuyoah/XRouter/v8/sdk/proxyutil"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// utlsRoundTripper 使用 Chrome 指纹实现 http.RoundTripper，用于
// 需要类浏览器 TLS 的供应商。HTTP 版本遵循 ALPN。每个
// 请求都获得一条专用连接，随响应体一起关闭。
type utlsRoundTripper struct {
	dialer proxy.Dialer
}

type closeConnectionBody struct {
	io.ReadCloser
	closeConnection func() error
	once            sync.Once
	err             error
}

func (b *closeConnectionBody) Close() error {
	if b == nil {
		return nil
	}
	b.once.Do(func() {
		var errConnection error
		if b.closeConnection != nil {
			errConnection = b.closeConnection()
		}
		var errBody error
		if b.ReadCloser != nil {
			errBody = b.ReadCloser.Close()
		}
		b.err = errors.Join(errBody, errConnection)
	})
	return b.err
}

func newUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return &utlsRoundTripper{dialer: dialer}
}

func (t *utlsRoundTripper) createConnection(ctx context.Context, host, addr string) (*tls.UConn, error) {
	contextDialer, ok := t.dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("utls: dialer does not support context cancellation")
	}
	conn, errDial := contextDialer.DialContext(ctx, "tcp", addr)
	if errDial != nil {
		return nil, fmt.Errorf("utls: dial upstream: %w", errDial)
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

	handshakeCtx, cancelHandshake := handshakeContext(ctx)
	defer cancelHandshake()
	if errHandshake := tlsConn.HandshakeContext(handshakeCtx); errHandshake != nil {
		errHandshake = classifyHandshakeError(ctx, handshakeCtx, errHandshake)
		if errors.Is(errHandshake, context.Canceled) || errors.Is(errHandshake, context.DeadlineExceeded) {
			return nil, fmt.Errorf("utls: TLS handshake: %w", errHandshake)
		}
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("utls: TLS handshake: %w; close connection: %v", errHandshake, errClose)
		}
		return nil, fmt.Errorf("utls: TLS handshake: %w", errHandshake)
	}

	return tlsConn, nil
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	tlsConn, err := t.createConnection(req.Context(), hostname, addr)
	if err != nil {
		return nil, err
	}
	return roundTripUtlsConnection(req, tlsConn)
}

// roundTripUtlsConnection 在发送请求前选择 HTTP 协议。
// 空的 ALPN 即 HTTP/1.1，包括 TLS 检查代理省略 ALPN 的情形。
func roundTripUtlsConnection(req *http.Request, tlsConn *tls.UConn) (*http.Response, error) {
	closeConnection := func() error {
		// HTTP/1.1 传输可能已在读取响应或取消请求后
		// 关闭了其非池化连接。
		if errClose := tlsConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			return errClose
		}
		return nil
	}
	var resp *http.Response
	var err error
	switch protocol := tlsConn.ConnectionState().NegotiatedProtocol; protocol {
	case "h2":
		h2Conn, errClientConn := (&http2.Transport{}).NewClientConn(tlsConn)
		if errClientConn != nil {
			err = fmt.Errorf("utls: initialize HTTP/2 connection: %w", errClientConn)
			break
		}
		closeConnection = h2Conn.Close
		resp, err = h2Conn.RoundTrip(req)
	case "", "http/1.1":
		// 复用已经完成握手的 uTLS 连接。一个新建的、非池化的
		// 传输保留了 net/http 的取消和请求体处理，
		// 而不改变 TLS 指纹也不经由其他路径重拨。
		transport := &http.Transport{
			DisableKeepAlives: true,
			DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
				return tlsConn, nil
			},
		}
		resp, err = transport.RoundTrip(req)
		transport.CloseIdleConnections()
	default:
		err = fmt.Errorf("utls: unsupported negotiated protocol %q", protocol)
	}
	if err != nil {
		if errClose := closeConnection(); errClose != nil {
			log.Debugf("utls: close connection after round trip failure: %v", errClose)
		}
		return nil, err
	}
	if resp == nil {
		if errClose := closeConnection(); errClose != nil {
			log.Debugf("utls: close connection after empty response: %v", errClose)
		}
		return nil, fmt.Errorf("utls: upstream returned an empty response")
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	resp.Body = &closeConnectionBody{
		ReadCloser:      resp.Body,
		closeConnection: closeConnection,
	}
	return resp, nil
}

// claudeCodeSessionCacheCapacity 为 Anthropic 推理面的
// 每传输 TLS 会话缓存设限。
const claudeCodeSessionCacheCapacity = 32

// newClaudeCodeTLSConfig 为一次推理面拨号构建 uTLS 配置。
//
// OmitEmptyPsk 让 pre_shared_key 扩展在会话被缓存之前保持静默，
// 因此未恢复的 ClientHello 与捕获到的原生握手逐字节一致。
// PreferSkipResumptionOnNilExtension 把 uTLS 的 HelloCustom
// "在没有匹配扩展的情况下恢复"的 panic 转成跳过恢复。
func newClaudeCodeTLSConfig(host string, sessionCache tls.ClientSessionCache) *tls.Config {
	return newClaudeCodeTLSConfigWithRoots(host, sessionCache, nil)
}

func newClaudeCodeTLSConfigWithRoots(host string, sessionCache tls.ClientSessionCache, roots *x509.CertPool) *tls.Config {
	return &tls.Config{
		ServerName:                         host,
		RootCAs:                            roots,
		ClientSessionCache:                 sessionCache,
		OmitEmptyPsk:                       true,
		PreferSkipResumptionOnNilExtension: true,
	}
}

// claudeCodeTLSClientHelloSpec 复现 Claude Code 2.1.220 在 macOS arm64 上
// 发出的确定性 Node/OpenSSL ClientHello。每当对外声明的 Claude Code 版本
// 发生变化时，都要让此 spec 与新的原生
// 捕获保持同步。
func claudeCodeTLSClientHelloSpec() *tls.ClientHelloSpec {
	return &tls.ClientHelloSpec{
		CipherSuites: []uint16{
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
		CompressionMethods: []uint8{0},
		Extensions: []tls.TLSExtension{
			&tls.SNIExtension{},
			&tls.ExtendedMasterSecretExtension{},
			&tls.RenegotiationInfoExtension{Renegotiation: tls.RenegotiateOnceAsClient},
			&tls.SupportedCurvesExtension{Curves: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}},
			&tls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&tls.SessionTicketExtension{},
			&tls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
			&tls.StatusRequestExtension{},
			&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []tls.SignatureScheme{
				tls.ECDSAWithP256AndSHA256,
				tls.PSSWithSHA256,
				tls.PKCS1WithSHA256,
				tls.ECDSAWithP384AndSHA384,
				tls.PSSWithSHA384,
				tls.PKCS1WithSHA384,
				tls.PSSWithSHA512,
				tls.PKCS1WithSHA512,
				tls.PKCS1WithSHA1,
			}},
			&tls.SCTExtension{},
			&tls.KeyShareExtension{KeyShares: []tls.KeyShare{{Group: tls.X25519}}},
			&tls.PSKKeyExchangeModesExtension{Modes: []uint8{tls.PskModeDHE}},
			&tls.SupportedVersionsExtension{Versions: []uint16{tls.VersionTLS13, tls.VersionTLS12}},
			&tls.UtlsPaddingExtension{GetPaddingLen: tls.BoringPaddingStyle},
			// pre_shared_key 必须是最后一个扩展（RFC 8446 4.2.11），位于
			// padding 之后。在缓存会话存在之前它贡献零字节。
			&tls.UtlsPreSharedKeyExtension{},
		},
	}
}

const claudeCodeRoundTripperCacheCapacity = 64

var claudeCodeRoundTripperCache = internalcache.NewBoundedLRU[string, http.RoundTripper](
	claudeCodeRoundTripperCacheCapacity,
	func(_ string, roundTripper http.RoundTripper) {
		if transport, ok := roundTripper.(interface{ CloseIdleConnections() }); ok {
			transport.CloseIdleConnections()
		}
	},
)

var claudeCodeMessagesHeaderOrder = []string{
	"Accept",
	"Authorization",
	"Content-Type",
	"User-Agent",
	"X-Claude-Code-Session-Id",
	"X-Stainless-Arch",
	"X-Stainless-Lang",
	"X-Stainless-OS",
	"X-Stainless-Package-Version",
	"X-Stainless-Retry-Count",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"X-Stainless-Timeout",
	"anthropic-beta",
	"anthropic-dangerous-direct-browser-access",
	"anthropic-version",
	"x-app",
	"x-client-request-id",
	"Connection",
	"Host",
	"Accept-Encoding",
	"Content-Length",
}

var claudeCodeCountTokensHeaderOrder = []string{
	"Accept",
	"Authorization",
	"Content-Type",
	"User-Agent",
	"X-Claude-Code-Session-Id",
	"X-Stainless-Arch",
	"X-Stainless-Lang",
	"X-Stainless-OS",
	"X-Stainless-Package-Version",
	"X-Stainless-Retry-Count",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"anthropic-beta",
	"anthropic-dangerous-direct-browser-access",
	"anthropic-version",
	"x-app",
	"x-client-request-id",
	"Connection",
	"Host",
	"Accept-Encoding",
	"Content-Length",
}

func claudeCodeRequestHeaderOrder(_, requestTarget string) []string {
	if strings.HasPrefix(requestTarget, "/v1/messages/count_tokens") {
		return claudeCodeCountTokensHeaderOrder
	}
	return claudeCodeMessagesHeaderOrder
}

func cachedClaudeCodeRoundTripper(proxyURL string) http.RoundTripper {
	return claudeCodeRoundTripperCache.GetOrAdd(proxyURL, func() http.RoundTripper {
		return newClaudeCodeRoundTripper(proxyURL)
	})
}

func newClaudeCodeRoundTripper(proxyURL string) http.RoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("claude tls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return newClaudeCodeRoundTripperForDialer(dialer)
}

// newClaudeCodeRoundTripperForDialer 在已解析好的 dialer 之上
// 构建 Claude Code 传输。它从
// newClaudeCodeRoundTripper 拆出，以便测试注入内存连接。
func newClaudeCodeRoundTripperForDialer(dialer proxy.Dialer) *http.Transport {
	return newClaudeCodeRoundTripperForDialerAndRoots(dialer, nil)
}

func newClaudeCodeRoundTripperForDialerAndRoots(dialer proxy.Dialer, roots *x509.CertPool) *http.Transport {
	// 该缓存的作用域限于此 round tripper，而它已按代理作为键，
	// 因此恢复绝不会跨越代理边界。
	sessionCache := tls.NewLRUClientSessionCache(claudeCodeSessionCacheCapacity)

	transport := &http.Transport{
		ForceAttemptHTTP2: false,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var (
				conn net.Conn
				err  error
			)
			if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
				conn, err = contextDialer.DialContext(ctx, network, addr)
			} else {
				conn, err = dialer.Dial(network, addr)
			}
			if err != nil {
				return nil, fmt.Errorf("claude tls: dial upstream: %w", err)
			}

			host, _, errSplit := net.SplitHostPort(addr)
			if errSplit != nil {
				if errClose := conn.Close(); errClose != nil {
					log.Debugf("claude tls: close failed connection: %v", errClose)
				}
				return nil, fmt.Errorf("claude tls: split upstream address: %w", errSplit)
			}
			tlsConn := tls.UClient(conn, newClaudeCodeTLSConfigWithRoots(host, sessionCache, roots), tls.HelloCustom)
			if errPreset := tlsConn.ApplyPreset(claudeCodeTLSClientHelloSpec()); errPreset != nil {
				if errClose := tlsConn.Close(); errClose != nil {
					log.Debugf("claude tls: close connection after preset failure: %v", errClose)
				}
				return nil, fmt.Errorf("claude tls: apply Claude Code ClientHello: %w", errPreset)
			}
			handshakeCtx, cancelHandshake := handshakeContext(ctx)
			defer cancelHandshake()
			if errHandshake := tlsConn.HandshakeContext(handshakeCtx); errHandshake != nil {
				errHandshake = classifyHandshakeError(ctx, handshakeCtx, errHandshake)
				if errClose := tlsConn.Close(); errClose != nil {
					log.Debugf("claude tls: close connection after handshake failure: %v", errClose)
				}
				return nil, fmt.Errorf("claude tls: handshake upstream: %w", errHandshake)
			}
			return httpwire.NewOrderedRequestConn(tlsConn, claudeCodeRequestHeaderOrder), nil
		},
	}
	return transport
}

// fallbackRoundTripper 对受保护的 HTTPS 主机使用供应商特定的
// TLS 指纹，并对所有其他请求回退到标准传输。
type fallbackRoundTripper struct {
	anthropic http.RoundTripper
	chrome    http.RoundTripper
	fallback  http.RoundTripper
}

// IsAnthropicUpstreamURL 报告一个已解析的请求是否指向 Anthropic 的
// 一方 API 源。Claude 格式的上游行为必须使用此门控，
// 使其不会漂移到自定义端口或包含 userinfo 的 URL。
func IsAnthropicUpstreamURL(u *url.URL) bool {
	if u == nil || u.User != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), "api.anthropic.com") {
		return false
	}
	port := u.Port()
	return port == "" || port == "443"
}

func (f *fallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if IsAnthropicUpstreamURL(req.URL) {
		return f.anthropic.RoundTrip(req)
	}
	if req.URL.Scheme == "https" && strings.EqualFold(req.URL.Hostname(), "chatgpt.com") {
		return f.chrome.RoundTrip(req)
	}
	return f.fallback.RoundTrip(req)
}

// NewUtlsHTTPClient 使用供应商特定的 TLS 指纹为受保护主机
// 创建一个 HTTP 客户端。它对 Anthropic 使用 Claude Code 的 Node/OpenSSL
// 配置文件，对 ChatGPT 使用 Chrome 配置文件，并对其他主机
// 回退到标准传输。
func NewUtlsHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) (clientOut *http.Client) {
	// 在请求作用域的包装器上携带可选的 TLS 建立时限（requests
	// tls-handshake-timeout-seconds）。此客户端是共享的，
	// 因此该时限有意同时作用于 Claude Code 的 Anthropic 拨号路径
	// 和 Chrome 的 ChatGPT/Codex 拨号路径；标准回退传输
	// 会忽略该上下文值。uTLS 拨号路径
	// 每次拨号都会读取它，因此值变化时按代理缓存的传输无需重建。
	// 禁用的时限（0）不改变请求。
	var handshakeTimeout time.Duration
	if cfg != nil {
		handshakeTimeout = cfg.TLSHandshakeTimeout()
	}
	defer func() {
		clientOut = httptransport.Decorate(ctx, clientOut)
		if clientOut != nil && handshakeTimeout > 0 {
			clientOut.Transport = &tlsHandshakeTimeoutRoundTripper{base: clientOut.Transport, timeout: handshakeTimeout}
		}
	}()
	proxyURL := effectiveProxyURL(ctx, cfg, auth)

	var ctxRoundTripper http.RoundTripper
	if ctx != nil {
		ctxRoundTripper, _ = ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
	}

	var chromeRT http.RoundTripper = newUtlsRoundTripper(proxyURL)
	var anthropicRT http.RoundTripper = cachedClaudeCodeRoundTripper(proxyURL)
	var standardTransport http.RoundTripper = http.DefaultTransport
	if proxyURL != "" {
		if transport := buildProxyTransport(proxyURL); transport != nil {
			standardTransport = transport
		}
	} else if ctxRoundTripper != nil {
		chromeRT = ctxRoundTripper
		anthropicRT = ctxRoundTripper
		standardTransport = ctxRoundTripper
	}

	client := &http.Client{
		Transport: &fallbackRoundTripper{
			anthropic: anthropicRT,
			chrome:    chromeRT,
			fallback:  standardTransport,
		},
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}

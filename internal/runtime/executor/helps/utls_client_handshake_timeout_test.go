package helps

import (
	"bufio"
	"context"
	gotls "crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/xnuyoah/XRouter/v8/internal/config"
	cliproxyauth "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executor"
)

// silentTLSListener 接受一个 TCP 连接，读取并丢弃 TLS ClientHello，
// 然后永远不返回任何内容，复现 Issue #6615 中
// 静默上游的形态。返回的通道在客户端一侧关闭连接后
// 关闭（读取端观察到 EOF），这同时证明失败的
// socket 已被释放。
func silentTLSListener(t *testing.T) (addr string, drained <-chan struct{}) {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	t.Cleanup(func() {
		if errClose := listener.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close listener: %v", errClose)
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			return
		}
		defer func() {
			// 客户端可能已经重置了这个被有意挂起的
			// 连接，因此其关闭结果不属于断言内容。
			_ = conn.Close()
		}()
		_, _ = io.Copy(io.Discard, conn)
	}()
	return listener.Addr().String(), done
}

// TestClaudeCodeTLSHandshakeTimeoutBoundsSilentPeer
// 通过真实的 Claude Code 传输复现 Issue #6615：
// 对端完成 TCP 连接但从不回应 TLS ClientHello。
// 在配置了选择性启用的握手超时后，连接建立必须在
// 有界的时间区间内以 deadline 错误失败，以便重试逻辑能够继续。
func TestClaudeCodeTLSHandshakeTimeoutBoundsSilentPeer(t *testing.T) {
	addr, drained := silentTLSListener(t)

	transport, ok := newClaudeCodeRoundTripper("").(*http.Transport)
	if !ok {
		t.Fatalf("Claude Code transport type = %T, want *http.Transport", newClaudeCodeRoundTripper(""))
	}
	ctx := withTLSHandshakeTimeout(context.Background(), 100*time.Millisecond)

	dialDone := make(chan error, 1)
	go func() {
		conn, errDial := transport.DialTLSContext(ctx, "tcp", addr)
		if conn != nil {
			errDial = errors.Join(errDial, conn.Close())
		}
		dialDone <- errDial
	}()

	select {
	case errDial := <-dialDone:
		if !errors.Is(errDial, context.DeadlineExceeded) {
			t.Fatalf("DialTLSContext error = %v, want context deadline exceeded", errDial)
		}
		if !cliproxyexecutor.IsConfiguredTLSHandshakeTimeoutError(errDial) {
			t.Fatalf("DialTLSContext error = %q, want typed configured TLS handshake timeout", errDial)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TLS handshake with silent peer was not bounded by the configured timeout")
	}

	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not observe the failed handshake connection being closed")
	}
}

// TestClaudeCodeTLSHandshakeTimeoutZeroKeepsUnboundedWait 验证
// 默认值（timeout 0）保持原有行为：在禁用该上限时，
// 静默对端会让握手一直保持挂起，只有调用方的
// 上下文取消才会结束等待。
func TestClaudeCodeTLSHandshakeTimeoutZeroKeepsUnboundedWait(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, io.ErrClosedPipe) && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close server connection: %v", errClose)
		}
	})

	dialer := contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	})
	transport := newClaudeCodeRoundTripperForDialer(dialer)

	ctx, cancel := context.WithCancel(withTLSHandshakeTimeout(context.Background(), 0))
	dialDone := make(chan error, 1)
	go func() {
		conn, errDial := transport.DialTLSContext(ctx, "tcp", "api.anthropic.com:443")
		if conn != nil {
			errDial = errors.Join(errDial, conn.Close())
		}
		dialDone <- errDial
	}()

	// 读取完整的 ClientHello 记录，从而确认握手
	// 正在进行并阻塞在读取对端的应答上。
	header := make([]byte, 5)
	if _, errRead := io.ReadFull(serverConn, header); errRead != nil {
		t.Fatalf("read ClientHello record header: %v", errRead)
	}
	recordLength := int(header[3])<<8 | int(header[4])
	if _, errRead := io.ReadFull(serverConn, make([]byte, recordLength)); errRead != nil {
		t.Fatalf("read ClientHello record body: %v", errRead)
	}

	select {
	case errDial := <-dialDone:
		t.Fatalf("DialTLSContext returned before caller cancellation: %v", errDial)
	default:
	}

	cancel()
	select {
	case errDial := <-dialDone:
		if !errors.Is(errDial, context.Canceled) {
			t.Fatalf("DialTLSContext error = %v, want context canceled", errDial)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TLS handshake did not stop after caller context cancellation")
	}
}

// TestUtlsRoundTripperHandshakeTimeoutBoundsSilentPeer 覆盖 Chrome
// 指纹路径（ChatGPT）下的相同静默对端形态：当
// 调用方配置了该上限时，createConnection 也必须在
// 有界区间内失败，而不是无限等待。
func TestUtlsRoundTripperHandshakeTimeoutBoundsSilentPeer(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, io.ErrClosedPipe) && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close server connection: %v", errClose)
		}
	})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		_, _ = io.Copy(io.Discard, serverConn)
	}()

	trackedConn := &trackedNetConn{Conn: clientConn}
	roundTripper := &utlsRoundTripper{dialer: contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		return trackedConn, nil
	})}
	ctx := withTLSHandshakeTimeout(context.Background(), 100*time.Millisecond)

	connectionDone := make(chan error, 1)
	go func() {
		tlsConn, errConnect := roundTripper.createConnection(ctx, "chatgpt.com", "chatgpt.com:443")
		if tlsConn != nil {
			errConnect = errors.Join(errConnect, tlsConn.Close())
		}
		connectionDone <- errConnect
	}()

	select {
	case errConnect := <-connectionDone:
		if !errors.Is(errConnect, context.DeadlineExceeded) {
			t.Fatalf("createConnection error = %v, want context deadline exceeded", errConnect)
		}
		if !cliproxyexecutor.IsConfiguredTLSHandshakeTimeoutError(errConnect) {
			t.Fatalf("createConnection error = %q, want typed configured TLS handshake timeout", errConnect)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Chrome-path TLS handshake with silent peer was not bounded by the configured timeout")
	}
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not observe the failed handshake connection being closed")
	}
}

// TestUtlsRoundTripperWithoutHandshakeBoundWaitsForCaller 对应 Chrome 路径上
// 的零值/禁用行为：在没有 ctx 载体的情况下，
// 握手会持续等待，只有调用方取消才会结束它，与
// TestUtlsRoundTripperHandshakeUsesRequestContext 完全一致。
func TestUtlsRoundTripperWithoutHandshakeBoundWaitsForCaller(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, io.ErrClosedPipe) && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close server connection: %v", errClose)
		}
	})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		_, _ = io.Copy(io.Discard, serverConn)
	}()

	roundTripper := &utlsRoundTripper{dialer: contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	})}
	ctx, cancel := context.WithCancel(context.Background())

	connectionDone := make(chan error, 1)
	go func() {
		tlsConn, errConnect := roundTripper.createConnection(ctx, "chatgpt.com", "chatgpt.com:443")
		if tlsConn != nil {
			errConnect = errors.Join(errConnect, tlsConn.Close())
		}
		connectionDone <- errConnect
	}()

	// 此时握手必须仍在进行中；只有下面显式的 cancel
	// 才能结束它。该路径上不存在确定性的「握手已开始」
	// 信号，因此这个检查有意只做单侧判断：它
	// 仅在握手已自行终止时失败。
	select {
	case errConnect := <-connectionDone:
		t.Fatalf("createConnection returned before caller cancellation: %v", errConnect)
	default:
	}

	cancel()
	select {
	case errConnect := <-connectionDone:
		if !errors.Is(errConnect, context.Canceled) {
			t.Fatalf("createConnection error = %v, want context canceled", errConnect)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Chrome-path TLS handshake did not stop after caller context cancellation")
	}
}

// deadlineRecordingConn 记录连接 deadline，使成功的
// 握手测试能够证明没有残留的建立超时被安装。
type deadlineRecordingConn struct {
	net.Conn
	deadlines chan time.Time
}

func (c *deadlineRecordingConn) SetDeadline(deadline time.Time) error {
	c.deadlines <- deadline
	return c.Conn.SetDeadline(deadline)
}

func (c *deadlineRecordingConn) SetReadDeadline(deadline time.Time) error {
	c.deadlines <- deadline
	return c.Conn.SetReadDeadline(deadline)
}

func (c *deadlineRecordingConn) SetWriteDeadline(deadline time.Time) error {
	c.deadlines <- deadline
	return c.Conn.SetWriteDeadline(deadline)
}

// TestClaudeCodeTLSHandshakeTimeoutPreservesSuccessfulConnection 在 synctest 中
// 通过 net.Pipe 演练一次真实成功的 Claude uTLS 握手。
// synctest 仅在每个 goroutine 都持久阻塞时才推进其虚拟时钟，
// 因此下面一秒的响应延迟证明了超出 100ms 握手上限的定时器
// 在已建立的连接被使用之前触发；该延迟不消耗任何
// 测试的墙上时钟时间。
func TestClaudeCodeTLSHandshakeTimeoutPreservesSuccessfulConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// synctest 从自己的虚拟纪元开始，因此要基于
		// 该时钟而非宿主墙上时钟来签发证书。
		certificate := newResumptionTestCertificateAt(t, time.Now())
		roots := x509.NewCertPool()
		roots.AddCert(certificate.Leaf)
		clientConn, serverConn := net.Pipe()
		t.Cleanup(func() {
			if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, io.ErrClosedPipe) && !errors.Is(errClose, net.ErrClosed) {
				t.Errorf("close server connection: %v", errClose)
			}
		})
		recorded := &deadlineRecordingConn{Conn: clientConn, deadlines: make(chan time.Time, 16)}
		dialer := contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
			return recorded, nil
		})
		transport := newClaudeCodeRoundTripperForDialerAndRoots(dialer, roots)
		const handshakeTimeout = 100 * time.Millisecond
		ctx := withTLSHandshakeTimeout(context.Background(), handshakeTimeout)

		dialDone := make(chan struct {
			conn net.Conn
			err  error
		}, 1)
		go func() {
			conn, errDial := transport.DialTLSContext(ctx, "tcp", "api.anthropic.com:443")
			dialDone <- struct {
				conn net.Conn
				err  error
			}{conn: conn, err: errDial}
		}()

		server := gotls.Server(serverConn, &gotls.Config{
			Certificates: []gotls.Certificate{certificate},
			MinVersion:   gotls.VersionTLS13,
			NextProtos:   []string{"http/1.1"},
		})
		serverDone := make(chan error, 1)
		go func() {
			if errHandshake := server.Handshake(); errHandshake != nil {
				serverDone <- fmt.Errorf("server handshake: %w", errHandshake)
				return
			}
			reader := bufio.NewReader(server)
			if _, errRead := http.ReadRequest(reader); errRead != nil {
				serverDone <- fmt.Errorf("server read request: %w", errRead)
				return
			}
			// 在所有 goroutine 阻塞时，虚拟时间推进到这个定时器，
			// 使响应投递远晚于握手上限。
			time.Sleep(time.Second)
			response := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}"
			if _, errWrite := io.WriteString(server, response); errWrite != nil {
				serverDone <- fmt.Errorf("server write response: %w", errWrite)
				return
			}
			// net.Pipe 不支持半关闭：TLS close_notify 写入会阻塞，
			// 直到对端再次读取，但本测试在 Content-Length 个字节后即停止。
			// 响应投递才是被测行为，因此在那些字节写入后即报告完成，
			// 并让清理逻辑关闭原始管道。
			serverDone <- nil
		}()

		var established net.Conn
		select {
		case result := <-dialDone:
			if result.err != nil {
				t.Fatalf("DialTLSContext error = %v, want successful handshake", result.err)
			}
			established = result.conn
		case <-time.After(5 * time.Second):
			t.Fatal("successful TLS handshake did not complete")
		}
		defer func() {
			// 先从另一端关闭原始管道。否则 uTLS 可能
			// 耗费自身五秒的告警超时，试图向一个
			// 在完整 HTTP 响应之后有意停止读取的
			// net.Pipe 对端写入 close_notify。
			if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) && !errors.Is(errClose, io.ErrClosedPipe) {
				t.Errorf("close server connection before client: %v", errClose)
			}
			if errClose := established.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) && !errors.Is(errClose, io.ErrClosedPipe) {
				t.Errorf("close established connection: %v", errClose)
			}
		}()

		select {
		case deadline := <-recorded.deadlines:
			t.Fatalf("successful handshake installed a residual connection deadline: %v", deadline)
		default:
		}
		if _, errWrite := io.WriteString(established, "GET /v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\nConnection: close\r\n\r\n"); errWrite != nil {
			t.Fatalf("write after successful handshake: %v", errWrite)
		}
		request, errRequest := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/messages", nil)
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		resp, errReadResponse := http.ReadResponse(bufio.NewReader(established), request)
		if errReadResponse != nil {
			t.Fatalf("read response beyond handshake bound: %v", errReadResponse)
		}
		body, errReadBody := io.ReadAll(resp.Body)
		if errReadBody != nil {
			t.Fatalf("read response body beyond handshake bound: %v", errReadBody)
		}
		if errCloseBody := resp.Body.Close(); errCloseBody != nil {
			t.Fatalf("close response body: %v", errCloseBody)
		}
		if resp.StatusCode != http.StatusOK || string(body) != "{}" {
			t.Fatalf("response = %d %q, want 200 {}", resp.StatusCode, body)
		}
		select {
		case errServer := <-serverDone:
			if errServer != nil {
				t.Fatalf("server completion: %v", errServer)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("TLS server did not finish")
		}
	})
}

// TestNewUtlsHTTPClientStampsConfiguredHandshakeBound 在客户端接缝处
// 端到端验证配置接线：当
// requests.tls-handshake-timeout-seconds 为正时，客户端发出的每个请求
// 都携带该上限，使 uTLS 拨号路径可以读取它；
// 当它为 0（或未提供配置）时，请求保持不携带该标记，
// 行为不变。
func TestNewUtlsHTTPClientStampsConfiguredHandshakeBound(t *testing.T) {
	capture := func(seen *time.Duration) http.RoundTripper {
		return utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			*seen = tlsHandshakeTimeoutFromContext(req.Context())
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("{}")),
				Request:    req,
			}, nil
		})
	}
	tests := []struct {
		name    string
		seconds int
		hasCfg  bool
		want    time.Duration
	}{
		{name: "positive seconds stamp the bound", seconds: 3, hasCfg: true, want: 3 * time.Second},
		{name: "zero leaves requests unstamped", seconds: 0, hasCfg: true, want: 0},
		{name: "negative leaves requests unstamped", seconds: -2, hasCfg: true, want: 0},
		{name: "nil config leaves requests unstamped", hasCfg: false, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := time.Duration(-1)
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", capture(&seen))
			var cfg *config.Config
			if tt.hasCfg {
				cfg = &config.Config{}
				cfg.TLSHandshakeTimeoutSeconds = tt.seconds
			}
			client := NewUtlsHTTPClient(ctx, cfg, &cliproxyauth.Auth{}, 0)
			resp, errGet := client.Get("https://api.anthropic.com/v1/messages")
			if errGet != nil {
				t.Fatalf("client.Get error = %v", errGet)
			}
			if errClose := resp.Body.Close(); errClose != nil {
				t.Fatalf("close response body: %v", errClose)
			}
			if seen != tt.want {
				t.Fatalf("request handshake bound = %v, want %v", seen, tt.want)
			}
		})
	}
}

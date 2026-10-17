package helps

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	gotls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	tls "github.com/refraction-networking/utls"
)

// newResumptionTestCertificate 为回环 TLS 服务器签发一个短期自签名叶证书，
// 该服务器用于会恢复（resumption）测试。
func newResumptionTestCertificate(t *testing.T) gotls.Certificate {
	return newResumptionTestCertificateAt(t, time.Now())
}

// newResumptionTestCertificateAt 签发一个在当前时间附近有效的测试叶证书。
// 在 synctest 内运行的测试会传入其伪造的时间基准，使证书校验
// 与握手计时器看到相同的时钟。
func newResumptionTestCertificateAt(t *testing.T, now time.Time) gotls.Certificate {
	t.Helper()
	key, errKey := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if errKey != nil {
		t.Fatalf("generate test key: %v", errKey)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "api.anthropic.com"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		DNSNames:     []string{"api.anthropic.com"},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, errCreate := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if errCreate != nil {
		t.Fatalf("create test certificate: %v", errCreate)
	}
	leaf, errParse := x509.ParseCertificate(der)
	if errParse != nil {
		t.Fatalf("parse test certificate: %v", errParse)
	}
	return gotls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// TestClaudeCodeTLSSessionResumptionCompletesHandshake 证明 Claude Code 的
// 推理 ClientHello 确实可以恢复会话：规范要求 pre_shared_key
// 位于 padding 扩展之后，因此顺序错误或 padding 交互异常
// 会在这里表现为握手失败，而不是悄无声息的回归。
func TestClaudeCodeTLSSessionResumptionCompletesHandshake(t *testing.T) {
	certificate := newResumptionTestCertificate(t)
	roots := x509.NewCertPool()
	roots.AddCert(certificate.Leaf)

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	t.Cleanup(func() {
		if errClose := listener.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close listener: %v", errClose)
		}
	})

	serverConfig := &gotls.Config{
		Certificates: []gotls.Certificate{certificate},
		MinVersion:   gotls.VersionTLS13,
	}
	go func() {
		for {
			raw, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go func(conn net.Conn) {
				server := gotls.Server(conn, serverConfig)
				if errHandshake := server.Handshake(); errHandshake != nil {
					_ = conn.Close()
					return
				}
				// 该问候消息会冲刷握手后的 NewSessionTicket
				// 消息，客户端需要它们才能恢复会话。
				_, _ = server.Write([]byte("ok\n"))
				_, _ = server.Read(make([]byte, 8))
				_ = server.Close()
			}(raw)
		}
	}()

	sessionCache := tls.NewLRUClientSessionCache(claudeCodeSessionCacheCapacity)
	dial := func(round int) (resumed bool, helloLength int) {
		raw, errDial := net.Dial("tcp", listener.Addr().String())
		if errDial != nil {
			t.Fatalf("round %d dial: %v", round, errDial)
		}
		defer func() {
			if errClose := raw.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
				t.Errorf("round %d close: %v", round, errClose)
			}
		}()

		config := newClaudeCodeTLSConfig("api.anthropic.com", sessionCache)
		config.RootCAs = roots
		conn := tls.UClient(raw, config, tls.HelloCustom)
		if errPreset := conn.ApplyPreset(claudeCodeTLSClientHelloSpec()); errPreset != nil {
			t.Fatalf("round %d apply preset: %v", round, errPreset)
		}
		if errHandshake := conn.Handshake(); errHandshake != nil {
			t.Fatalf("round %d handshake: %v", round, errHandshake)
		}
		helloLength = len(conn.HandshakeState.Hello.Raw)
		if _, errRead := conn.Read(make([]byte, 8)); errRead != nil && !errors.Is(errRead, io.EOF) {
			t.Fatalf("round %d read: %v", round, errRead)
		}
		_, _ = conn.Write([]byte("bye\n"))
		return conn.ConnectionState().DidResume, helloLength
	}

	firstResumed, firstLength := dial(1)
	if firstResumed {
		t.Fatal("first handshake reported resumption without a cached session")
	}
	secondResumed, secondLength := dial(2)
	if !secondResumed {
		t.Fatal("second handshake did not resume, so the session cache is not effective")
	}

	// padding 扩展会吸收 pre_shared_key 字节，因此恢复的
	// ClientHello 与全新的 ClientHello 保持相同的 BoringSSL padding 边界。
	if firstLength != secondLength {
		t.Fatalf("resumed ClientHello length = %d, want %d to match the fresh handshake", secondLength, firstLength)
	}
}

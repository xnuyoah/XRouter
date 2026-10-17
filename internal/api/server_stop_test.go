package api

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xnuyoah/XRouter/v8/internal/config"
)

func TestServerStop_ViolentShutdownImmediatelyClosesWithoutContextDeadlineError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 在随机可用端口上创建监听器。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port

	cfg := &config.Config{
		Host: "127.0.0.1",
		Port: port,
	}

	server := NewServer(cfg, nil, nil, "")
	engine := gin.New()
	reqStarted := make(chan struct{})
	engine.GET("/slow", func(c *gin.Context) {
		close(reqStarted)
		time.Sleep(2 * time.Second)
		c.String(http.StatusOK, "ok")
	})
	server.server.Handler = engine

	// 使用该监听器启动服务器。
	go func() {
		_ = server.server.Serve(ln)
	}()

	// 发起一个进行中的请求。
	reqDone := make(chan error, 1)
	go func() {
		resp, errGet := http.Get("http://" + ln.Addr().String() + "/slow")
		if errGet != nil {
			reqDone <- errGet
			return
		}
		defer func() {
			if errCloseBody := resp.Body.Close(); errCloseBody != nil {
				t.Logf("response body close error: %v", errCloseBody)
			}
		}()
		reqDone <- nil
	}()

	<-reqStarted

	// 使用已过期的 context 调用 server.Stop（模拟已过期的关闭期限）。
	// 在暴力关闭（s.server.Close）下，服务器应立即关闭进行中的
	// 连接，既不需要等待，也不返回 context deadline exceeded。
	expiredCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	stopStart := time.Now()
	errStop := server.Stop(expiredCtx)
	stopDuration := time.Since(stopStart)

	if stopDuration >= time.Second {
		t.Fatalf("server.Stop took %v, want immediate violent shutdown (<1s)", stopDuration)
	}
	if errStop != nil {
		t.Fatalf("server.Stop error = %v, want nil for violent shutdown", errStop)
	}

	// 验证进行中的请求已被终止 / 中断。
	select {
	case errReq := <-reqDone:
		if errReq == nil {
			t.Fatal("expected in-flight request to be interrupted/error, but it completed successfully")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for in-flight request to be interrupted by server.Stop")
	}
}

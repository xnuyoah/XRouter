package api

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

func normalizeHTTPServeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func normalizeListenerError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *Server) acceptMuxConnections(listener net.Listener, httpListener *muxListener) error {
	if s == nil || listener == nil {
		return net.ErrClosed
	}

	for {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			return errAccept
		}
		if conn == nil {
			continue
		}

		// 将每个连接分派到单独的 goroutine，这样缓慢/空闲的客户端
		// 就不会阻塞 accept 循环。此前，TLS 握手和
		// reader.Peek(1) 是内联执行的；一个从不发送字节的空闲 TCP 连接
		// 会让 Peek 无限期阻塞，导致所有后续连接
		// 都无法被接受（issue #3267）。
		go s.routeMuxConnection(conn, httpListener)
	}
}

// routeMuxConnection 执行逐连接的协议检测与路由。
func (s *Server) routeMuxConnection(conn net.Conn, httpListener *muxListener) {
	// 设置读取截止时间，使从不发送字节的空闲连接不会
	// 泄漏 goroutine 和文件描述符。一旦连接被成功路由到
	// 其处理器，该截止时间就会被清除。
	const muxSniffDeadline = 10 * time.Second
	_ = conn.SetReadDeadline(time.Now().Add(muxSniffDeadline))

	tlsConn, ok := conn.(*tls.Conn)
	if ok {
		if errHandshake := tlsConn.Handshake(); errHandshake != nil {
			if errClose := conn.Close(); errClose != nil {
				log.Errorf("failed to close connection after TLS handshake error: %v", errClose)
			}
			return
		}
		proto := strings.TrimSpace(tlsConn.ConnectionState().NegotiatedProtocol)
		if proto == "h2" || proto == "http/1.1" {
			if httpListener == nil {
				if errClose := conn.Close(); errClose != nil {
					log.Errorf("failed to close connection: %v", errClose)
				}
				return
			}
			if errPut := httpListener.Put(tlsConn); errPut != nil {
				if errClose := conn.Close(); errClose != nil {
					log.Errorf("failed to close connection after HTTP routing failure: %v", errClose)
				}
			} else {
				_ = conn.SetReadDeadline(time.Time{})
			}
			return
		}
	}

	reader := bufio.NewReader(conn)
	prefix, errPeek := reader.Peek(1)
	if errPeek != nil {
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("failed to close connection after protocol peek failure: %v", errClose)
		}
		return
	}

	if isRedisRESPPrefix(prefix[0]) {
		_ = conn.SetReadDeadline(time.Time{})
		s.handleRedisConnection(conn, reader)
		return
	}

	if httpListener == nil {
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("failed to close connection without HTTP listener: %v", errClose)
		}
		return
	}

	if errPut := httpListener.Put(&bufferedConn{Conn: conn, reader: reader}); errPut != nil {
		if errClose := conn.Close(); errClose != nil {
			log.Errorf("failed to close connection after HTTP routing failure: %v", errClose)
		}
	} else {
		_ = conn.SetReadDeadline(time.Time{})
	}
}

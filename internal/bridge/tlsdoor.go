package bridge

import (
	"bufio"
	"crypto/tls"
	"net"
	"sync"
	"time"
)

// Bedrock clients joining by address ask for https://host:port/v1/join first and only try plain
// http after about 20 seconds. sniffTLS lets the door answer both on one port: a connection that
// opens with a TLS handshake (first byte 0x16) is served over TLS, anything else as plain HTTP, so
// players typing a bare IP (no valid certificate) still get in.
func sniffTLS(ln net.Listener, config *tls.Config) net.Listener {
	return &sniffListener{Listener: ln, config: config, conns: make(chan net.Conn), done: make(chan struct{})}
}

type sniffListener struct {
	net.Listener
	config *tls.Config
	conns  chan net.Conn
	done   chan struct{}
	once   sync.Once
	err    error
}

func (l *sniffListener) Accept() (net.Conn, error) {
	l.once.Do(func() { go l.loop() })
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, l.err
	}
}

func (l *sniffListener) loop() {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			l.err = err
			close(l.done)
			return
		}
		go l.sniff(c)
	}
}

// sniff reads a connection's first byte, without consuming it, to tell TLS from plain HTTP.
func (l *sniffListener) sniff(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return
	}
	var conn net.Conn = &peekedConn{Conn: c, r: br}
	if first[0] == 0x16 {
		conn = tls.Server(conn, l.config)
	}
	select {
	case l.conns <- conn:
	case <-l.done:
		_ = c.Close()
	}
}

// peekedConn reads through the bufio.Reader that peeked at the connection's first byte.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

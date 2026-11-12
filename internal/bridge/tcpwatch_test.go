package bridge

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

type tcpWatchFake struct {
	net.Conn
	closed chan struct{}
}

func (f *tcpWatchFake) ReadPacket() ([]byte, error) { <-f.closed; return nil, net.ErrClosed }
func (f *tcpWatchFake) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}
func (f *tcpWatchFake) DisableEncryption() bool  { return true }
func (f *tcpWatchFake) DisableCompression() bool { return true }

func newWatched(ping func(context.Context, string) ([]byte, error)) (*tcpWatchConn, *tcpWatchFake) {
	f := &tcpWatchFake{closed: make(chan struct{})}
	w := &tcpWatchConn{backendConn: f, address: "x", stop: make(chan struct{}), ping: ping}
	w.lastRead.Store(time.Now().UnixNano())
	return w, f
}

func TestTCPWatchClosesDeadBackend(t *testing.T) {
	w, f := newWatched(func(context.Context, string) ([]byte, error) { return nil, errors.New("refused") })
	go w.watch(50*time.Millisecond, 10*time.Millisecond)
	select {
	case <-f.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("silent backend that fails its ping was not closed")
	}
	if _, err := w.ReadPacket(); err == nil {
		t.Fatal("read after close succeeded")
	}
}

func TestTCPWatchKeepsQuietLiveBackend(t *testing.T) {
	w, f := newWatched(func(context.Context, string) ([]byte, error) { return []byte("pong"), nil })
	go w.watch(30*time.Millisecond, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	select {
	case <-f.closed:
		t.Fatal("quiet backend that answers pings was closed")
	default:
	}
	_ = w.Close()
	if !w.DisableEncryption() || !w.DisableCompression() {
		t.Fatal("wrapper hides the transport's encryption/compression switches")
	}
}

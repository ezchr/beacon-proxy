package bridge

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// A TCP backend link has no keepalive of its own (RakNet and NetherNet time out a silent peer), so a
// backend that freezes without closing its socket would keep the player on it forever: no read error
// ever reaches readBackend, so the hold never starts. tcpWatchConn watches the link: after
// tcpBackendIdle without a frame it pings the backend on a new connection, and closes the link when the
// ping fails, which ends the reader the same way a backend restart does. A quiet but healthy backend (a
// world without a time cycle can send nothing for a long time) still answers the ping and is kept.
// A failed write (gophertunnel's tcpConn times writes out after 10 s) also closes the link.
const (
	tcpBackendIdle      = 30 * time.Second
	tcpBackendPingLimit = 5 * time.Second
)

type tcpWatchConn struct {
	backendConn
	address  string
	log      *slog.Logger
	lastRead atomic.Int64 // unix nanoseconds of the last frame read
	stop     chan struct{}
	once     sync.Once

	// ping pings the backend; pingGameServer over TCP outside tests.
	ping func(ctx context.Context, address string) ([]byte, error)
}

func watchTCPBackend(c backendConn, address string, log *slog.Logger) *tcpWatchConn {
	w := &tcpWatchConn{backendConn: c, address: address, log: log, stop: make(chan struct{}), ping: func(ctx context.Context, address string) ([]byte, error) {
		return pingGameServer(ctx, TransportTCP, address)
	}}
	w.lastRead.Store(time.Now().UnixNano())
	go w.watch(tcpBackendIdle, tcpBackendIdle/3)
	return w
}

func (w *tcpWatchConn) watch(idle, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, w.lastRead.Load())) < idle {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), tcpBackendPingLimit)
		_, err := w.ping(ctx, w.address)
		cancel()
		if err == nil {
			// Alive, just quiet: wait another idle period before asking again.
			w.lastRead.Store(time.Now().UnixNano())
			continue
		}
		if w.log != nil {
			w.log.Warn("tcp backend: no frame and no ping answer, closing the link", "addr", w.address, "idle", idle, "err", err)
		}
		_ = w.Close()
		return
	}
}

func (w *tcpWatchConn) ReadPacket() ([]byte, error) {
	b, err := w.backendConn.ReadPacket()
	if err == nil {
		w.lastRead.Store(time.Now().UnixNano())
	}
	return b, err
}

func (w *tcpWatchConn) Read(b []byte) (int, error) {
	n, err := w.backendConn.Read(b)
	if err == nil {
		w.lastRead.Store(time.Now().UnixNano())
	}
	return n, err
}

func (w *tcpWatchConn) Write(b []byte) (int, error) {
	n, err := w.backendConn.Write(b)
	if err != nil {
		// A frame may be half written: the link cannot be used any more.
		_ = w.Close()
	}
	return n, err
}

func (w *tcpWatchConn) Close() error {
	w.once.Do(func() { close(w.stop) })
	return w.backendConn.Close()
}

// The methods below pass on what the packet encoder, decoder and the relay look for on the wrapped
// connection, which embedding the backendConn interface would hide.

func (w *tcpWatchConn) DisableEncryption() bool {
	d, ok := w.backendConn.(interface{ DisableEncryption() bool })
	return ok && d.DisableEncryption()
}

func (w *tcpWatchConn) DisableCompression() bool {
	d, ok := w.backendConn.(interface{ DisableCompression() bool })
	return ok && d.DisableCompression()
}

func (w *tcpWatchConn) Latency() time.Duration {
	if l, ok := w.backendConn.(interface{ Latency() time.Duration }); ok {
		return l.Latency()
	}
	return 0
}

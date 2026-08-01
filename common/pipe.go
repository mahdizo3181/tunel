package common

import (
	"io"
	"sync"
)

// halfCloser is implemented by *net.TCPConn (and similar) to shut down only
// the write side, leaving the read side open.
type halfCloser interface {
	CloseWrite() error
}

// closeWrite half-closes c if it supports it (plain TCP conns), otherwise
// falls back to a full Close. yamux streams don't implement halfCloser but
// their Close is already a proper local-only half-close (it still permits
// reads until the remote side finishes), so the fallback is safe there too.
func closeWrite(c io.ReadWriteCloser) {
	if hc, ok := c.(halfCloser); ok {
		hc.CloseWrite()
		return
	}
	c.Close()
}

// Pipe splices a and b together bidirectionally using pooled buffers, and
// blocks until both directions have finished. Each direction is
// half-closed (not fully closed) as soon as its source hits EOF, so a
// fast-finishing request side can't tear down the connection out from under
// a still-in-flight response on the other side; both ends are only fully
// closed once both directions have completed.
func Pipe(a, b io.ReadWriteCloser) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		buf := getBuf()
		defer putBuf(buf)
		io.CopyBuffer(a, b, *buf)
		closeWrite(a)
	}()

	go func() {
		defer wg.Done()
		buf := getBuf()
		defer putBuf(buf)
		io.CopyBuffer(b, a, *buf)
		closeWrite(b)
	}()

	wg.Wait()
	a.Close()
	b.Close()
}

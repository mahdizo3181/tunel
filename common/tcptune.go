package common

import "net"

// TuneTCPConn adjusts the kernel socket buffers (SO_RCVBUF / SO_SNDBUF) so
// the tunnel can sustain high throughput without relying on small default
// buffers to throttle it. No-op for non-TCP connections.
func TuneTCPConn(conn net.Conn, bufBytes int) {
	if bufBytes <= 0 {
		return
	}
	tc, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tc.SetReadBuffer(bufBytes)
	_ = tc.SetWriteBuffer(bufBytes)
	_ = tc.SetNoDelay(true)
}

// tunedListener wraps a net.Listener and applies TuneTCPConn to every
// accepted connection.
type tunedListener struct {
	net.Listener
	bufBytes int
}

func TunedListener(inner net.Listener, bufBytes int) net.Listener {
	return &tunedListener{Listener: inner, bufBytes: bufBytes}
}

func (l *tunedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	TuneTCPConn(c, l.bufBytes)
	return c, nil
}

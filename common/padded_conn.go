package common

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// PaddedConn wraps a *websocket.Conn and presents it as an
// io.ReadWriteCloser suitable for handing to yamux. Every WebSocket binary
// message on the wire is:
//
//	[2 bytes big-endian real length][real payload][random padding bytes]
//
// The random chunk sizing (splitting large writes into several
// variably-sized messages) and random padding tail mean consecutive tunnel
// frames never share a stable length signature, defeating packet-length
// based DPI classifiers. A zero-length real payload is a pure padding/noise
// frame and is silently discarded by the reader; nothing in this package
// currently emits one, but the wire format reserves it for future chaff
// traffic.
type PaddedConn struct {
	ws      *websocket.Conn
	pad     PaddingConfig
	rng     *rand.Rand
	rngMu   sync.Mutex
	writeMu sync.Mutex

	readBuf []byte

	closeOnce sync.Once
	closed    chan struct{}

	writeTimeout time.Duration
}

func NewPaddedConn(ws *websocket.Conn, pad PaddingConfig) *PaddedConn {
	pad.ApplyDefaults()
	return &PaddedConn{
		ws:           ws,
		pad:          pad,
		rng:          rand.New(rand.NewSource(time.Now().UnixNano())),
		closed:       make(chan struct{}),
		writeTimeout: 15 * time.Second,
	}
}

func (c *PaddedConn) randRange(min, max int) int {
	if max <= min {
		return min
	}
	c.rngMu.Lock()
	defer c.rngMu.Unlock()
	return min + c.rng.Intn(max-min+1)
}

func (c *PaddedConn) Read(p []byte) (int, error) {
	for len(c.readBuf) == 0 {
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			return 0, err
		}
		if len(msg) < 2 {
			continue
		}
		realLen := int(binary.BigEndian.Uint16(msg[0:2]))
		if realLen > len(msg)-2 {
			return 0, errors.New("padded_conn: corrupt frame length")
		}
		if realLen == 0 {
			continue // pure padding/keepalive noise frame
		}
		c.readBuf = msg[2 : 2+realLen]
	}
	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

func (c *PaddedConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	written := 0
	for written < len(p) {
		remaining := p[written:]
		chunkSize := c.randRange(c.pad.MinChunk, c.pad.MaxChunk)
		if chunkSize > len(remaining) {
			chunkSize = len(remaining)
		}
		padLen := c.randRange(c.pad.MinPadBytes, c.pad.MaxPadBytes)

		buf := make([]byte, 2+chunkSize+padLen)
		binary.BigEndian.PutUint16(buf[0:2], uint16(chunkSize))
		copy(buf[2:2+chunkSize], remaining[:chunkSize])
		if padLen > 0 {
			c.rngMu.Lock()
			for i := 2 + chunkSize; i < len(buf); i++ {
				buf[i] = byte(c.rng.Intn(256))
			}
			c.rngMu.Unlock()
		}

		c.writeMu.Lock()
		c.ws.SetWriteDeadline(time.Now().Add(c.writeTimeout))
		err := c.ws.WriteMessage(websocket.BinaryMessage, buf)
		c.writeMu.Unlock()
		if err != nil {
			return written, err
		}
		written += chunkSize
	}
	return written, nil
}

func (c *PaddedConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.ws.Close()
	})
	return err
}

// StartKeepalive begins an aggressive WebSocket-level ping/pong loop. If a
// pong is not received within pongTimeout, the read deadline set by the
// pong handler expires, the in-flight (or next) Read fails, and the caller
// (yamux) tears the session down — which is what triggers the client's
// reconnect-with-backoff logic. onDead (optional) is invoked exactly once
// when the keepalive loop exits due to error.
func (c *PaddedConn) StartKeepalive(pingInterval, pongTimeout time.Duration, onDead func(error)) {
	c.ws.SetReadDeadline(time.Now().Add(pongTimeout))
	c.ws.SetPongHandler(func(string) error {
		c.ws.SetReadDeadline(time.Now().Add(pongTimeout))
		return nil
	})

	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-c.closed:
				return
			case <-ticker.C:
				c.writeMu.Lock()
				err := c.ws.WriteControl(websocket.PingMessage, []byte("hb"), time.Now().Add(5*time.Second))
				c.writeMu.Unlock()
				if err != nil {
					c.Close()
					if onDead != nil {
						onDead(fmt.Errorf("keepalive ping failed: %w", err))
					}
					return
				}
			}
		}
	}()
}

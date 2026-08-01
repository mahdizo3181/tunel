package common

import (
	"io"
	"time"

	"github.com/hashicorp/yamux"
)

// YamuxConfig returns a yamux.Config tuned for an aggressive, self-healing
// tunnel: a short keepalive interval so a dead underlying WebSocket is
// detected quickly (in addition to the WSS-level ping/pong in
// PaddedConn), and a generous accept backlog so a burst of local
// connections doesn't stall waiting for stream setup.
func YamuxConfig(keepaliveInterval time.Duration) *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = keepaliveInterval
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.AcceptBacklog = 256
	cfg.LogOutput = io.Discard
	return cfg
}

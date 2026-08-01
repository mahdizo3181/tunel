// Command bridge is the tunnel client: it runs in the unrestricted region
// and actively dials out to the gate (through a CDN), so the gate never
// needs an inbound port open for the tunnel itself. Once connected it
// accepts multiplexed streams and forwards each to a local target
// (typically a VPN panel listening on localhost).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"tunel/common"
)

func hostPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	return net.JoinHostPort(u.Hostname(), "443")
}

func connectAndServe(ctx context.Context, cfg *ClientConfig) error {
	u, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return fmt.Errorf("parse server_url: %w", err)
	}
	addr := hostPort(u)
	sni := cfg.SNI
	if sni == "" {
		sni = u.Hostname()
	}

	header := http.Header{}
	header.Set(cfg.AuthHeader, cfg.AuthToken)

	// The Dialer's NetDialTLSContext hook is what lets us hand gorilla an
	// already-established uTLS connection: with it set, Dial trusts that
	// TLS is already done and skips wrapping the conn with crypto/tls
	// itself (which would otherwise attempt a second, conflicting TLS
	// handshake on top of ours).
	dialer := &websocket.Dialer{
		NetDialTLSContext: func(ctx context.Context, network, netAddr string) (net.Conn, error) {
			return common.DialUTLS(ctx, netAddr, sni, cfg.TCPBufferBytes, cfg.InsecureSkipVerify)
		},
		HandshakeTimeout: 15 * time.Second,
		ReadBufferSize:   8192,
		WriteBufferSize:  8192,
	}

	ws, resp, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return fmt.Errorf("websocket handshake: %w", err)
	}

	pc := common.NewPaddedConn(ws, cfg.Padding)
	keepaliveErr := make(chan error, 1)
	pc.StartKeepalive(
		time.Duration(cfg.PingIntervalSeconds)*time.Second,
		time.Duration(cfg.PongTimeoutSeconds)*time.Second,
		func(err error) {
			select {
			case keepaliveErr <- err:
			default:
			}
		},
	)

	session, err := yamux.Server(pc, common.YamuxConfig(time.Duration(cfg.YamuxKeepaliveSeconds)*time.Second))
	if err != nil {
		pc.Close()
		return fmt.Errorf("yamux setup: %w", err)
	}
	defer session.Close()

	log.Printf("tunnel established to %s", addr)
	connectedAt := time.Now()

	go func() {
		<-ctx.Done()
		session.Close()
	}()

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case kaErr := <-keepaliveErr:
				return fmt.Errorf("session ended: %w (accept: %v)", kaErr, err)
			default:
			}
			if time.Since(connectedAt) >= time.Duration(cfg.StableConnMillis)*time.Millisecond {
				return fmt.Errorf("%w", errStableSessionEnded)
			}
			return fmt.Errorf("accept stream: %w", err)
		}
		go handleStream(stream, cfg)
	}
}

// errStableSessionEnded signals a session that lived long enough to be
// considered healthy before dying, so the reconnect loop can reset backoff
// to its minimum instead of continuing to back off.
var errStableSessionEnded = errors.New("stable session ended")

func handleStream(stream *yamux.Stream, cfg *ClientConfig) {
	defer stream.Close()
	target, err := net.DialTimeout("tcp", cfg.TargetAddr, 10*time.Second)
	if err != nil {
		log.Printf("dial target %s failed: %v", cfg.TargetAddr, err)
		return
	}
	common.TuneTCPConn(target, cfg.TCPBufferBytes)
	common.Pipe(stream, target)
}

func backoffDelay(attempt, minMs, maxMs int) time.Duration {
	d := minMs << attempt
	if d <= 0 || d > maxMs { // overflow or cap
		d = maxMs
	}
	jitter := rand.Intn(d/2 + 1)
	return time.Duration(d/2+jitter) * time.Millisecond
}

func main() {
	configPath := flag.String("config", "config.client.json", "path to client config JSON")
	flag.Parse()

	cfg, err := LoadClientConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}

		err := connectAndServe(ctx, cfg)
		if ctx.Err() != nil {
			log.Printf("shutting down")
			return
		}

		if errors.Is(err, errStableSessionEnded) {
			log.Printf("tunnel session ended after a stable run, reconnecting immediately")
			attempt = 0
			continue
		}

		log.Printf("tunnel error: %v", err)
		delay := backoffDelay(attempt, cfg.ReconnectMinBackoffMillis, cfg.ReconnectMaxBackoffMillis)
		log.Printf("reconnecting in %s", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
		if attempt < 30 {
			attempt++
		}
	}
}

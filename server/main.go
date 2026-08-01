// Command gate is the tunnel server: it runs in the restricted region,
// listens on 443 behind a CDN, accepts the reverse tunnel dialed in by the
// bridge (client), and forwards locally-accepted user connections through
// that tunnel.
package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"tunel/common"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  8192,
	WriteBufferSize: 8192,
	// The origin normally only ever sees the CDN's IP as the connecting
	// peer, so Origin header checks provide no real security here; the
	// auth header/token is the actual gate.
	CheckOrigin: func(r *http.Request) bool { return true },
}

type gate struct {
	cfg          *ServerConfig
	decoy        http.Handler
	sessionMu    sync.RWMutex
	session      *yamux.Session
	sessionReady chan struct{} // closed & replaced each time a session becomes available
}

func newGate(cfg *ServerConfig) *gate {
	return &gate{
		cfg:          cfg,
		decoy:        buildDecoyHandler(cfg.DecoyHTMLPath),
		sessionReady: make(chan struct{}),
	}
}

func (g *gate) setSession(s *yamux.Session) {
	g.sessionMu.Lock()
	old := g.session
	g.session = s
	ready := g.sessionReady
	g.sessionReady = make(chan struct{})
	g.sessionMu.Unlock()
	if s != nil {
		close(ready)
	}
	if old != nil {
		old.Close()
	}
}

func (g *gate) clearSession(s *yamux.Session) {
	g.sessionMu.Lock()
	if g.session == s {
		g.session = nil
	}
	g.sessionMu.Unlock()
}

// waitForSession blocks until a tunnel session is available or timeout
// elapses, so a burst of user connections arriving just before/during a
// bridge reconnect doesn't get spuriously dropped.
func (g *gate) waitForSession(timeout time.Duration) *yamux.Session {
	g.sessionMu.RLock()
	s := g.session
	ready := g.sessionReady
	g.sessionMu.RUnlock()
	if s != nil {
		return s
	}
	select {
	case <-ready:
	case <-time.After(timeout):
		return nil
	}
	g.sessionMu.RLock()
	defer g.sessionMu.RUnlock()
	return g.session
}

func validToken(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get(g.cfg.AuthHeader)
	isUpgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")

	if r.URL.Path != g.cfg.WSPath || !isUpgrade || !validToken(token, g.cfg.AuthToken) {
		g.decoy.ServeHTTP(w, r)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade failed from %s: %v", r.RemoteAddr, err)
		return
	}
	go g.handleTunnelConn(ws)
}

func (g *gate) handleTunnelConn(ws *websocket.Conn) {
	log.Printf("bridge connected, establishing tunnel session (remote=%s)", ws.RemoteAddr())

	pc := common.NewPaddedConn(ws, g.cfg.Padding)
	pc.StartKeepalive(
		time.Duration(g.cfg.PingIntervalSeconds)*time.Second,
		time.Duration(g.cfg.PongTimeoutSeconds)*time.Second,
		func(err error) { log.Printf("tunnel keepalive failed: %v", err) },
	)

	// The gate is the side that actively opens streams (one per
	// incoming user connection), so it takes the yamux "client" role
	// even though, at the TCP/WS layer, it is the one being dialed
	// into. Stream direction and connection direction are independent.
	session, err := yamux.Client(pc, common.YamuxConfig(time.Duration(g.cfg.YamuxKeepaliveSeconds)*time.Second))
	if err != nil {
		log.Printf("yamux session setup failed: %v", err)
		pc.Close()
		return
	}

	g.setSession(session)
	<-session.CloseChan()
	g.clearSession(session)
	log.Printf("tunnel session closed (remote=%s)", ws.RemoteAddr())
}

func (g *gate) handleUserConn(conn net.Conn) {
	defer conn.Close()

	sess := g.waitForSession(time.Duration(g.cfg.SessionWaitMillis) * time.Millisecond)
	if sess == nil {
		log.Printf("no active tunnel session; dropping user connection from %s", conn.RemoteAddr())
		return
	}
	stream, err := sess.Open()
	if err != nil {
		log.Printf("failed to open tunnel stream: %v", err)
		return
	}
	common.Pipe(conn, stream)
}

func (g *gate) runForwardListener(ctx context.Context) error {
	ln, err := net.Listen("tcp", g.cfg.ForwardListenAddr)
	if err != nil {
		return err
	}
	ln = common.TunedListener(ln, g.cfg.TCPBufferBytes)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	log.Printf("forwarding listener up on %s", g.cfg.ForwardListenAddr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			log.Printf("forward listener accept error: %v", err)
			return err
		}
		go g.handleUserConn(conn)
	}
}

func main() {
	configPath := flag.String("config", "config.server.json", "path to server config JSON")
	flag.Parse()

	cfg, err := LoadServerConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	g := newGate(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := g.runForwardListener(ctx); err != nil {
			log.Printf("forward listener stopped: %v", err)
		}
	}()

	rawLn, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.ListenAddr, err)
	}
	tunedLn := common.TunedListener(rawLn, cfg.TCPBufferBytes)

	httpServer := &http.Server{
		Handler:           g,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		<-ctx.Done()
		httpServer.Close()
	}()

	if cfg.TLSCertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			log.Fatalf("load TLS keypair: %v", err)
		}
		tlsLn := tls.NewListener(tunedLn, &tls.Config{Certificates: []tls.Certificate{cert}})
		log.Printf("gate listening (TLS) on %s, ws_path=%s", cfg.ListenAddr, cfg.WSPath)
		err = httpServer.Serve(tlsLn)
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	} else {
		log.Printf("gate listening (PLAIN HTTP, expects CDN edge TLS) on %s, ws_path=%s", cfg.ListenAddr, cfg.WSPath)
		err = httpServer.Serve(tunedLn)
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}

	wg.Wait()
}

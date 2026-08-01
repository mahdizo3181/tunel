package main

import (
	"fmt"

	"tunel/common"
)

type ServerConfig struct {
	// ListenAddr is where the gate listens for the (CDN-fronted) WSS
	// tunnel connection. Must stay on the standard HTTPS port so the
	// external traffic profile is indistinguishable from normal web
	// traffic.
	ListenAddr string `json:"listen_addr"`

	// TLSCertFile/TLSKeyFile: origin certificate. Use a Cloudflare
	// Origin CA certificate here and set the zone's SSL/TLS mode to
	// "Full (strict)". Leave both empty only if the origin is meant to
	// speak plain HTTP behind a CDN doing edge TLS termination
	// ("Flexible" mode) - not recommended, see README.
	TLSCertFile string `json:"tls_cert_file"`
	TLSKeyFile  string `json:"tls_key_file"`

	// WSPath is the HTTP path that, combined with AuthHeader/AuthToken,
	// identifies a genuine tunnel handshake. Every other request gets
	// the decoy page.
	WSPath     string `json:"ws_path"`
	AuthHeader string `json:"auth_header"`
	AuthToken  string `json:"auth_token"`

	// ForwardListenAddr is the local port end users connect to; traffic
	// received here is carried over the tunnel and delivered to the
	// bridge's TargetAddr.
	ForwardListenAddr string `json:"forward_listen_addr"`

	// DecoyHTMLPath: optional path to a custom decoy page served to
	// anyone who isn't a valid tunnel handshake. Falls back to a bundled
	// generic "it works" page.
	DecoyHTMLPath string `json:"decoy_html_path"`

	Padding common.PaddingConfig `json:"padding"`

	PingIntervalSeconds   int `json:"ping_interval_seconds"`
	PongTimeoutSeconds    int `json:"pong_timeout_seconds"`
	YamuxKeepaliveSeconds int `json:"yamux_keepalive_seconds"`
	TCPBufferBytes        int `json:"tcp_buffer_bytes"`

	// SessionWaitMillis: how long an accepted local user connection will
	// wait for a live tunnel session before giving up.
	SessionWaitMillis int `json:"session_wait_millis"`
}

func LoadServerConfig(path string) (*ServerConfig, error) {
	cfg := &ServerConfig{}
	if err := common.LoadJSON(path, cfg); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *ServerConfig) applyDefaults() {
	if c.ListenAddr == "" {
		c.ListenAddr = "0.0.0.0:443"
	}
	if c.WSPath == "" {
		c.WSPath = "/ws"
	}
	if c.AuthHeader == "" {
		c.AuthHeader = "X-Tunnel-Auth"
	}
	c.AuthToken = common.EnvOrDefault("TUNNEL_AUTH_TOKEN", c.AuthToken)
	if c.PingIntervalSeconds <= 0 {
		c.PingIntervalSeconds = 10
	}
	if c.PongTimeoutSeconds <= 0 {
		c.PongTimeoutSeconds = 20
	}
	if c.YamuxKeepaliveSeconds <= 0 {
		c.YamuxKeepaliveSeconds = 15
	}
	if c.TCPBufferBytes <= 0 {
		c.TCPBufferBytes = 4 * 1024 * 1024
	}
	if c.SessionWaitMillis <= 0 {
		c.SessionWaitMillis = 5000
	}
	c.Padding.ApplyDefaults()
}

func (c *ServerConfig) validate() error {
	if c.AuthToken == "" {
		return fmt.Errorf("auth_token (or TUNNEL_AUTH_TOKEN env var) must be set")
	}
	if c.ForwardListenAddr == "" {
		return fmt.Errorf("forward_listen_addr must be set")
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("tls_cert_file and tls_key_file must both be set or both empty")
	}
	return nil
}

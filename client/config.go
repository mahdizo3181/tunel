package main

import (
	"fmt"

	"tunel/common"
)

type ClientConfig struct {
	// ServerURL is the CDN-fronted endpoint, e.g.
	// "wss://your-domain.example.com/ws". Its host is what actually gets
	// dialed (resolving to the CDN edge, not the real origin), and its
	// path must match the gate's ws_path.
	ServerURL string `json:"server_url"`

	// SNI overrides the TLS ServerName sent in the ClientHello. Defaults
	// to the ServerURL host if empty. Normally leave unset.
	SNI string `json:"sni"`

	AuthHeader string `json:"auth_header"`
	AuthToken  string `json:"auth_token"`

	// TargetAddr is the local service on this (unrestricted) machine
	// that tunnel traffic gets delivered to, e.g. "127.0.0.1:1194" for a
	// VPN panel.
	TargetAddr string `json:"target_addr"`

	InsecureSkipVerify bool `json:"insecure_skip_verify"`

	Padding common.PaddingConfig `json:"padding"`

	PingIntervalSeconds   int `json:"ping_interval_seconds"`
	PongTimeoutSeconds    int `json:"pong_timeout_seconds"`
	YamuxKeepaliveSeconds int `json:"yamux_keepalive_seconds"`
	TCPBufferBytes        int `json:"tcp_buffer_bytes"`

	ReconnectMinBackoffMillis int `json:"reconnect_min_backoff_millis"`
	ReconnectMaxBackoffMillis int `json:"reconnect_max_backoff_millis"`

	// StableConnMillis: a session that stays up at least this long is
	// considered "healthy", resetting the reconnect backoff to its
	// minimum on the next disconnect rather than continuing to grow.
	StableConnMillis int `json:"stable_conn_millis"`
}

func LoadClientConfig(path string) (*ClientConfig, error) {
	cfg := &ClientConfig{}
	if err := common.LoadJSON(path, cfg); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *ClientConfig) applyDefaults() {
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
	if c.ReconnectMinBackoffMillis <= 0 {
		c.ReconnectMinBackoffMillis = 1000
	}
	if c.ReconnectMaxBackoffMillis <= 0 {
		c.ReconnectMaxBackoffMillis = 30000
	}
	if c.StableConnMillis <= 0 {
		c.StableConnMillis = 60000
	}
	c.Padding.ApplyDefaults()
}

func (c *ClientConfig) validate() error {
	if c.ServerURL == "" {
		return fmt.Errorf("server_url must be set")
	}
	if c.AuthToken == "" {
		return fmt.Errorf("auth_token (or TUNNEL_AUTH_TOKEN env var) must be set")
	}
	if c.TargetAddr == "" {
		return fmt.Errorf("target_addr must be set")
	}
	return nil
}

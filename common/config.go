// Package common holds code shared by the gate (server) and bridge (client)
// binaries: framing/padding, buffer pooling, tuned dialing/listening, and
// the bidirectional pipe used to splice a local TCP connection onto a yamux
// stream.
package common

import (
	"encoding/json"
	"fmt"
	"os"
)

// PaddingConfig controls the randomized frame padding/chunking applied to
// every WebSocket message so that on-the-wire packet lengths never settle
// into a stable, fingerprintable pattern.
type PaddingConfig struct {
	MinPadBytes int `json:"min_pad_bytes"`
	MaxPadBytes int `json:"max_pad_bytes"`
	MinChunk    int `json:"min_chunk"`
	MaxChunk    int `json:"max_chunk"`
}

// DefaultPadding returns sane defaults if a config omits the section.
func DefaultPadding() PaddingConfig {
	return PaddingConfig{
		MinPadBytes: 16,
		MaxPadBytes: 256,
		MinChunk:    1024,
		MaxChunk:    8192,
	}
}

func (p *PaddingConfig) ApplyDefaults() {
	d := DefaultPadding()
	if p.MinPadBytes <= 0 && p.MaxPadBytes <= 0 {
		p.MinPadBytes, p.MaxPadBytes = d.MinPadBytes, d.MaxPadBytes
	}
	if p.MaxPadBytes < p.MinPadBytes {
		p.MaxPadBytes = p.MinPadBytes
	}
	if p.MinChunk <= 0 {
		p.MinChunk = d.MinChunk
	}
	if p.MaxChunk <= 0 || p.MaxChunk < p.MinChunk {
		p.MaxChunk = d.MaxChunk
	}
	// The wire header uses a uint16 length prefix per message.
	const maxAllowed = 65000
	if p.MaxChunk > maxAllowed {
		p.MaxChunk = maxAllowed
	}
}

// LoadJSON reads and decodes a JSON config file into dst (a pointer).
func LoadJSON(path string, dst interface{}) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open config %q: %w", path, err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("parse config %q: %w", path, err)
	}
	return nil
}

// EnvOrDefault returns the environment variable's value if set, else def.
// Used to let secrets (auth tokens) be injected via the environment instead
// of living in a config file on disk.
func EnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

package common

import (
	"context"
	"fmt"
	"net"
	"time"

	utls "github.com/refraction-networking/utls"
)

// DialUTLS performs a raw TCP dial followed by a TLS handshake whose
// ClientHello is byte-for-byte shaped like a real Chrome browser (extension
// order, curves, cipher suites, ALPN, GREASE, etc.), rather than Go's
// default crypto/tls ClientHello which DPI middleboxes fingerprint easily
// (JA3/JA4) as "Go program", not "browser". sni is the TLS ServerName /
// SNI value sent in the handshake (normally the CDN-fronted domain).
func DialUTLS(ctx context.Context, addr, sni string, tcpBufBytes int, insecureSkipVerify bool) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tcp dial %s: %w", addr, err)
	}
	TuneTCPConn(raw, tcpBufBytes)

	uConfig := &utls.Config{
		ServerName:         sni,
		InsecureSkipVerify: insecureSkipVerify,
	}
	uconn := utls.UClient(raw, uConfig, utls.HelloChrome_Auto)

	if err := uconn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("utls handshake to %s (sni=%s): %w", addr, sni, err)
	}
	return uconn, nil
}

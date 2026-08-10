package security

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

type ssrfLookupIPAddrFunc func(context.Context, string) ([]net.IPAddr, error)

func ssrfPinnedDialAddress(ctx context.Context, address string, lookup ssrfLookupIPAddrFunc) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("SSRF protection: invalid dial address: %w", err)
	}
	var addresses []net.IPAddr
	if parsed := net.ParseIP(host); parsed != nil {
		addresses = []net.IPAddr{{IP: parsed}}
	} else {
		addresses, err = lookup(ctx, host)
		if err != nil {
			return "", fmt.Errorf("SSRF protection: DNS resolution failed: %w", err)
		}
	}
	if len(addresses) == 0 {
		return "", fmt.Errorf("SSRF protection: hostname resolved to no IP addresses")
	}
	for _, candidate := range addresses {
		if result := DefaultIPValidator.ValidateIP(candidate.IP); !result.Valid {
			return "", fmt.Errorf("SSRF protection: %s", result.Reason)
		}
	}
	return net.JoinHostPort(addresses[0].IP.String(), port), nil
}

// NewSSRFSafeHTTPClient returns the normal TLS-hardened client with a dialer
// that validates every DNS answer and connects to the exact IP it validated.
// Callers must still validate the user-facing URL before presenting/using it;
// the dialer closes the DNS-rebinding gap between that review and the socket.
func NewSSRFSafeHTTPClient() (*http.Client, error) {
	client, err := CreateDefaultHTTPClient()
	if err != nil {
		return nil, err
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("secure HTTP transport is unavailable")
	}
	clone := transport.Clone()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	clone.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		pinned, err := ssrfPinnedDialAddress(ctx, address, net.DefaultResolver.LookupIPAddr)
		if err != nil {
			return nil, err
		}
		return dialer.DialContext(ctx, network, pinned)
	}
	client.Transport = clone
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client, nil
}

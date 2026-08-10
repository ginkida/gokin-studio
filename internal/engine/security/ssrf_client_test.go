package security

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestSSRFPinnedDialAddressValidatesEveryAnswerAndPinsFirst(t *testing.T) {
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("1.1.1.1")}}, nil
	}
	got, err := ssrfPinnedDialAddress(context.Background(), "public.example:443", lookup)
	if err != nil || got != "93.184.216.34:443" {
		t.Fatalf("pinned address = %q, %v", got, err)
	}
	rebound := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}
	if _, err := ssrfPinnedDialAddress(context.Background(), "rebind.example:80", rebound); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("mixed public/private DNS answer was accepted: %v", err)
	}
	if _, err := ssrfPinnedDialAddress(context.Background(), "metadata.example:80", func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("resolver unavailable")
	}); err == nil || !strings.Contains(err.Error(), "DNS") {
		t.Fatalf("resolver failure was accepted: %v", err)
	}
}

func TestNewSSRFSafeHTTPClientDoesNotFollowRedirects(t *testing.T) {
	client, err := NewSSRFSafeHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.Transport.(*http.Transport); !ok {
		t.Fatalf("transport = %T", client.Transport)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.com/next", nil)
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect result = %v", err)
	}
}

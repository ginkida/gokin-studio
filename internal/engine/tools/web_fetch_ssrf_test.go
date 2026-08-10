package tools

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestSSRFSafeDialAddressPinsValidatedPublicIP(t *testing.T) {
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}, {IP: net.ParseIP("2606:4700:4700::1111")}}, nil
	}
	got, err := ssrfSafeDialAddress(context.Background(), "public.example:443", lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.1.1.1:443" {
		t.Fatalf("pinned address = %q", got)
	}
}

func TestSSRFSafeDialAddressRejectsAnyPrivateResolution(t *testing.T) {
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}
	if _, err := ssrfSafeDialAddress(context.Background(), "rebind.example:80", lookup); err == nil ||
		!strings.Contains(err.Error(), "blocked IP") {
		t.Fatalf("mixed public/private resolution was accepted: %v", err)
	}
	if _, err := ssrfSafeDialAddress(context.Background(), "169.254.169.254:80", lookup); err == nil {
		t.Fatal("raw metadata IP was accepted")
	}
}

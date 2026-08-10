package security

import (
	"net"
	"testing"
)

func TestIPValidatorAllowsPublicIPv4Representations(t *testing.T) {
	validator := NewIPValidator()
	for _, raw := range []string{"1.1.1.1", "::ffff:1.1.1.1"} {
		if result := validator.ValidateIP(net.ParseIP(raw)); !result.Valid {
			t.Fatalf("public address %s was blocked: %s", raw, result.Reason)
		}
	}
}

func TestIPValidatorBlocksPrivateIPv4Representations(t *testing.T) {
	validator := NewIPValidator()
	for _, raw := range []string{
		"127.0.0.1", "::ffff:127.0.0.1",
		"10.0.0.1", "::ffff:10.0.0.1",
		"169.254.169.254", "::ffff:169.254.169.254",
	} {
		if result := validator.ValidateIP(net.ParseIP(raw)); result.Valid {
			t.Fatalf("private address %s was allowed", raw)
		}
	}
}

// The unspecified addresses are not "no host" to the kernel: connecting to
// 0.0.0.0 or :: reaches a loopback listener, which would otherwise expose the
// preview proxy, the OAuth callback server, and the user's dev servers.
func TestIPValidatorBlocksUnspecifiedAddresses(t *testing.T) {
	validator := NewIPValidator()
	for _, raw := range []string{"0.0.0.0", "::ffff:0.0.0.0", "0.0.0.255", "::"} {
		if result := validator.ValidateIP(net.ParseIP(raw)); result.Valid {
			t.Fatalf("unspecified address %s was allowed", raw)
		}
	}
	for _, raw := range []string{"http://0.0.0.0:8080/", "http://[::]:8080/"} {
		if result := ValidateURLForSSRF(raw); result.Valid {
			t.Fatalf("unspecified URL %s was allowed", raw)
		}
	}
}

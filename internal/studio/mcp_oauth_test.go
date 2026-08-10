package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMCPOAuthCredentialStoreRoundTrip(t *testing.T) {
	if os.Getenv("GOKIN_TEST_OS_CREDENTIAL_STORE") != "1" {
		t.Skip("set GOKIN_TEST_OS_CREDENTIAL_STORE=1 for the opt-in OS credential-store integration test")
	}
	suffix, err := randomOAuthValue(12)
	if err != nil {
		t.Fatal(err)
	}
	name := "credential-store-test-" + suffix
	secret := []byte(`{"accessToken":"test-only-secret","version":1}`)
	defer func() {
		if err := deleteMCPOAuthCredential(name); err != nil {
			t.Errorf("cleanup credential: %v", err)
		}
	}()
	if err := saveMCPOAuthCredential(name, secret); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadMCPOAuthCredential(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded, secret) {
		t.Fatalf("credential changed: %q", loaded)
	}
	if err := deleteMCPOAuthCredential(name); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMCPOAuthCredential(name); !errors.Is(err, errMCPOAuthCredentialNotFound) {
		t.Fatalf("load after delete error = %v", err)
	}
	// The deferred cleanup is intentionally idempotent.
}

func writeOAuthJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthChallengeParameter(t *testing.T) {
	header := `Bearer realm="example", resource_metadata="https://auth.example/.well-known/oauth-protected-resource", error="invalid_token"`
	if got := oauthChallengeParameter(header, "resource_metadata"); got != "https://auth.example/.well-known/oauth-protected-resource" {
		t.Fatalf("resource_metadata = %q", got)
	}
	if got := oauthChallengeParameter(`Bearer resource_metadata="https://example.test/a\,b"`, "resource_metadata"); got != `https://example.test/a,b` {
		t.Fatalf("escaped resource_metadata = %q", got)
	}
}

func TestProtectedResourceMetadataCandidates(t *testing.T) {
	got, err := protectedResourceMetadataCandidates("https://mcp.example.test/team/mcp")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://mcp.example.test/.well-known/oauth-protected-resource/team/mcp",
		"https://mcp.example.test/.well-known/oauth-protected-resource",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestMCPOAuthDiscoveryBlocksPublicToPrivatePivot(t *testing.T) {
	if err := validateDiscoveredMCPOAuthURL(
		"https://mcp.example.test/mcp",
		"http://127.0.0.1:7777/.well-known/oauth-protected-resource",
		"protected-resource metadata",
	); err == nil || !strings.Contains(err.Error(), "local/private") {
		t.Fatalf("public-to-private discovery error = %v", err)
	}
	if err := validateDiscoveredMCPOAuthURL(
		"http://127.0.0.1:3000/mcp",
		"http://localhost:8080/.well-known/oauth-authorization-server",
		"authorization server",
	); err != nil {
		t.Fatalf("local development discovery rejected: %v", err)
	}
}

func TestDiscoverMCPOAuthMetadata(t *testing.T) {
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+server.URL+`/.well-known/oauth-protected-resource/mcp"`)
		http.Error(w, "authorization required", http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeOAuthJSON(t, w, mcpOAuthProtectedResource{
			Resource:             server.URL + "/mcp",
			AuthorizationServers: []string{server.URL + "/issuer"},
			ScopesSupported:      []string{"mcp:tools"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/issuer", func(w http.ResponseWriter, r *http.Request) {
		writeOAuthJSON(t, w, mcpOAuthAuthorizationServer{
			Issuer:                        server.URL + "/issuer",
			AuthorizationEndpoint:         server.URL + "/authorize",
			TokenEndpoint:                 server.URL + "/token",
			RegistrationEndpoint:          server.URL + "/register",
			CodeChallengeMethodsSupported: []string{"S256"},
		})
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	protected, err := discoverMCPProtectedResource(context.Background(), server.Client(), server.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if protected.Resource != server.URL+"/mcp" || len(protected.AuthorizationServers) != 1 {
		t.Fatalf("protected resource = %#v", protected)
	}
	authorization, err := discoverMCPAuthorizationServer(context.Background(), server.Client(), protected.AuthorizationServers[0])
	if err != nil {
		t.Fatal(err)
	}
	if authorization.TokenEndpoint != server.URL+"/token" || !containsOAuthValue(authorization.CodeChallengeMethodsSupported, "S256") {
		t.Fatalf("authorization server = %#v", authorization)
	}
}

func TestDiscoverMCPOAuthRejectsAudienceMismatch(t *testing.T) {
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+server.URL+`/.well-known/oauth-protected-resource/mcp"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeOAuthJSON(t, w, mcpOAuthProtectedResource{
			Resource:             server.URL + "/other",
			AuthorizationServers: []string{server.URL},
		})
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	_, err := discoverMCPProtectedResource(context.Background(), server.Client(), server.URL+"/mcp")
	if err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("audience mismatch error = %v", err)
	}
}

func TestAuthorizeMCPServerPKCEAndSecureSave(t *testing.T) {
	s := newStudioForTest(t)
	var server *httptest.Server
	var mu sync.Mutex
	var registeredRedirect string
	var browserChallenge string
	var savedName string
	var savedCredential []byte
	browserResult := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+server.URL+`/.well-known/oauth-protected-resource/mcp"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeOAuthJSON(t, w, mcpOAuthProtectedResource{
			Resource:             server.URL + "/mcp",
			AuthorizationServers: []string{server.URL + "/issuer"},
			ScopesSupported:      []string{"mcp:tools"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/issuer", func(w http.ResponseWriter, r *http.Request) {
		writeOAuthJSON(t, w, mcpOAuthAuthorizationServer{
			Issuer:                        server.URL + "/issuer",
			AuthorizationEndpoint:         server.URL + "/authorize",
			TokenEndpoint:                 server.URL + "/token",
			RegistrationEndpoint:          server.URL + "/register",
			CodeChallengeMethodsSupported: []string{"S256"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("registration method = %s", r.Method)
		}
		var body struct {
			RedirectURIs            []string `json:"redirect_uris"`
			TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 32<<10)).Decode(&body); err != nil {
			t.Errorf("decode registration: %v", err)
		}
		if len(body.RedirectURIs) != 1 || body.TokenEndpointAuthMethod != "none" {
			t.Errorf("registration body = %#v", body)
		} else {
			mu.Lock()
			registeredRedirect = body.RedirectURIs[0]
			mu.Unlock()
		}
		writeOAuthJSON(t, w, map[string]any{
			"client_id":                  "gokin-public-client",
			"token_endpoint_auth_method": "none",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		mu.Lock()
		redirect := registeredRedirect
		challenge := browserChallenge
		mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("grant_type") != "authorization_code" ||
			r.Form.Get("client_id") != "gokin-public-client" ||
			r.Form.Get("resource") != server.URL+"/mcp" ||
			r.Form.Get("redirect_uri") != redirect ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			t.Errorf("invalid token exchange form: %#v", r.Form)
		}
		writeOAuthJSON(t, w, map[string]any{
			"access_token":  "access-secret",
			"refresh_token": "refresh-secret",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "mcp:tools",
		})
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	if err := s.SaveMCPServer(MCPServerConfig{
		Name: "oauth-test", Transport: mcpTransportHTTP, URL: server.URL + "/mcp",
		AuthType: mcpAuthOAuth, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	s.testMCPOAuthHTTPClient = server.Client()
	s.testMCPOAuthSave = func(name string, data []byte) error {
		savedName = name
		savedCredential = append([]byte(nil), data...)
		return nil
	}
	s.testMCPOAuthOpenBrowser = func(target string) error {
		authorizationURL, err := url.Parse(target)
		if err != nil {
			return err
		}
		query := authorizationURL.Query()
		if query.Get("response_type") != "code" ||
			query.Get("client_id") != "gokin-public-client" ||
			query.Get("resource") != server.URL+"/mcp" ||
			query.Get("scope") != "mcp:tools" ||
			query.Get("code_challenge_method") != "S256" {
			return fmt.Errorf("invalid authorization query: %v", query)
		}
		mu.Lock()
		browserChallenge = query.Get("code_challenge")
		redirect := registeredRedirect
		mu.Unlock()
		go func() {
			callback, err := url.Parse(redirect)
			if err == nil {
				values := callback.Query()
				values.Set("code", "authorization-code")
				values.Set("state", query.Get("state"))
				callback.RawQuery = values.Encode()
				var response *http.Response
				response, err = http.Get(callback.String()) // #nosec G107 -- loopback URL created by the test flow
				if response != nil {
					_ = response.Body.Close()
				}
			}
			browserResult <- err
		}()
		return nil
	}

	result, err := s.AuthorizeMCPServer("oauth-test")
	if err != nil {
		t.Fatal(err)
	}
	if callbackErr := <-browserResult; callbackErr != nil {
		t.Fatal(callbackErr)
	}
	if !result.Authorized || result.AuthorizationServer != server.URL+"/issuer" ||
		savedName != "oauth-test" {
		t.Fatalf("authorization result = %#v, savedName=%q", result, savedName)
	}
	var stored mcpOAuthCredential
	if err := json.Unmarshal(savedCredential, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "access-secret" || stored.RefreshToken != "refresh-secret" ||
		stored.Resource != server.URL+"/mcp" || stored.ClientID != "gokin-public-client" {
		t.Fatalf("stored credential = %#v", stored)
	}
	configData, err := os.ReadFile(mcpConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "access-secret") || strings.Contains(string(configData), "refresh-secret") {
		t.Fatalf("MCP config leaked OAuth token: %s", configData)
	}
}

func TestValidateMCPHTTPConfigOAuthGuards(t *testing.T) {
	_, err := validateMCPConfig(MCPServerConfig{
		Name: "bad", Transport: mcpTransportHTTP, URL: "https://example.test/mcp",
		AuthType: mcpAuthOAuth, Headers: map[string]string{"Authorization": "Bearer secret"},
	})
	if err == nil || !strings.Contains(err.Error(), "Authorization header") {
		t.Fatalf("OAuth + Authorization header error = %v", err)
	}
	cfg, err := validateMCPConfig(MCPServerConfig{
		Name: "good", Transport: mcpTransportHTTP, URL: "https://example.test/mcp",
		AuthType: mcpAuthOAuth, OAuthClientID: "public-client",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthType != mcpAuthOAuth || cfg.OAuthClientID != "public-client" {
		t.Fatalf("validated OAuth config = %#v", cfg)
	}
}

func TestResolveMCPOAuthAccessTokenRefreshRotation(t *testing.T) {
	var server *httptest.Server
	requests := 0
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse refresh form: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" ||
			r.Form.Get("refresh_token") != "old-refresh" ||
			r.Form.Get("client_id") != "public-client" ||
			r.Form.Get("resource") != server.URL+"/mcp" {
			t.Errorf("refresh form = %#v", r.Form)
		}
		writeOAuthJSON(t, w, map[string]any{
			"access_token":  "new-access",
			"refresh_token": "rotated-refresh",
			"token_type":    "Bearer",
			"expires_in":    1800,
		})
	}))
	defer server.Close()

	var saved mcpOAuthCredential
	access, err := resolveMCPOAuthAccessToken(
		context.Background(),
		server.Client(),
		MCPServerConfig{
			Name: "refresh-test", Transport: mcpTransportHTTP,
			URL: server.URL + "/mcp", AuthType: mcpAuthOAuth,
		},
		mcpOAuthCredential{
			Version: 1, Resource: server.URL + "/mcp", ClientID: "public-client",
			TokenEndpoint: server.URL + "/token", AccessToken: "expired-access",
			RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(),
		},
		func(name string, data []byte) error {
			if name != "refresh-test" {
				t.Errorf("save name = %q", name)
			}
			return json.Unmarshal(data, &saved)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if access != "new-access" || requests != 1 ||
		saved.RefreshToken != "rotated-refresh" || saved.ClientID != "public-client" ||
		saved.Resource != server.URL+"/mcp" {
		t.Fatalf("refresh result access=%q requests=%d saved=%#v", access, requests, saved)
	}
}

func TestMCPOAuthCallbackRejectsWrongStateThenAccepts(t *testing.T) {
	redirect, results, closeServer, err := startMCPOAuthCallback("expected-state")
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer()
	client := &http.Client{Timeout: 2 * time.Second}
	wrong, err := client.Get(redirect + "?code=bad&state=wrong")
	if err != nil {
		t.Fatal(err)
	}
	_ = wrong.Body.Close()
	if wrong.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong-state status = %d", wrong.StatusCode)
	}
	good, err := client.Get(redirect + "?code=good&state=expected-state")
	if err != nil {
		t.Fatal(err)
	}
	_ = good.Body.Close()
	if good.StatusCode != http.StatusOK {
		t.Fatalf("good-state status = %d", good.StatusCode)
	}
	select {
	case result := <-results:
		if result.Code != "good" || result.Error != "" {
			t.Fatalf("callback result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback result")
	}
}

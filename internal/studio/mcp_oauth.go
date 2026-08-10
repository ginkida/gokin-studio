package studio

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	mcpAuthHeaders = "headers"
	mcpAuthOAuth   = "oauth"

	maxMCPOAuthMetadataBytes = 256 << 10
	maxMCPOAuthTokenBytes    = 64 << 10
	mcpOAuthCallbackTimeout  = 5 * time.Minute
)

type mcpOAuthProtectedResource struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

type mcpOAuthAuthorizationServer struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	ScopesSupported               []string `json:"scopes_supported"`
}

type mcpOAuthCredential struct {
	Version             int    `json:"version"`
	Resource            string `json:"resource"`
	AuthorizationServer string `json:"authorizationServer"`
	ClientID            string `json:"clientID"`
	TokenEndpoint       string `json:"tokenEndpoint"`
	AccessToken         string `json:"accessToken"`
	RefreshToken        string `json:"refreshToken,omitempty"`
	Scope               string `json:"scope,omitempty"`
	ExpiresAt           int64  `json:"expiresAt,omitempty"`
}

type MCPAuthorizationResult struct {
	Authorized          bool     `json:"authorized"`
	AuthorizationServer string   `json:"authorizationServer"`
	Scopes              []string `json:"scopes,omitempty"`
	ExpiresAt           int64    `json:"expiresAt,omitempty"`
}

type mcpOAuthCallback struct {
	Code  string
	Error string
}

var mcpOAuthRefreshMu sync.Mutex

func newMCPOAuthHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        8,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("OAuth redirects are disabled for metadata and token requests")
		},
	}
}

func validateMCPOAuthURL(raw, purpose string) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > 4096 || strings.ContainsRune(raw, 0) {
		return nil, fmt.Errorf("invalid %s URL", purpose)
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return nil, fmt.Errorf("%s URL must be absolute", purpose)
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s URL cannot contain credentials or a fragment", purpose)
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, fmt.Errorf("%s URL must use HTTPS (or HTTP on localhost)", purpose)
		}
	default:
		return nil, fmt.Errorf("%s URL must use HTTPS (or HTTP on localhost)", purpose)
	}
	return parsed, nil
}

func canonicalMCPResource(raw string) (string, error) {
	parsed, err := validateMCPOAuthURL(raw, "MCP resource")
	if err != nil {
		return "", err
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

func oauthURLIsLocalOrPrivate(parsed *url.URL) bool {
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast())
}

func validateDiscoveredMCPOAuthURL(source, target, purpose string) error {
	sourceURL, err := validateMCPOAuthURL(source, "OAuth discovery source")
	if err != nil {
		return err
	}
	targetURL, err := validateMCPOAuthURL(target, purpose)
	if err != nil {
		return err
	}
	if oauthURLIsLocalOrPrivate(targetURL) && !oauthURLIsLocalOrPrivate(sourceURL) {
		return fmt.Errorf("%s cannot move OAuth discovery from a public server to a local/private address", purpose)
	}
	return nil
}

func readBoundedOAuthJSON(resp *http.Response, limit int64, out any) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("OAuth response exceeds %d KiB", limit>>10)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OAuth endpoint returned HTTP %d", resp.StatusCode)
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "json") {
		return fmt.Errorf("OAuth endpoint returned non-JSON content")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode OAuth response: %w", err)
	}
	return nil
}

func fetchMCPOAuthJSON(ctx context.Context, client *http.Client, rawURL string, out any) error {
	if _, err := validateMCPOAuthURL(rawURL, "OAuth metadata"); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gokin-studio/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	return readBoundedOAuthJSON(resp, maxMCPOAuthMetadataBytes, out)
}

// oauthChallengeParameter parses a quoted or token auth-param without
// treating commas inside quoted strings as separators.
func oauthChallengeParameter(header, wanted string) string {
	for i := 0; i < len(header); {
		for i < len(header) && (header[i] == ' ' || header[i] == '\t' || header[i] == ',') {
			i++
		}
		start := i
		for i < len(header) && (header[i] == '-' || header[i] == '_' ||
			header[i] >= 'a' && header[i] <= 'z' ||
			header[i] >= 'A' && header[i] <= 'Z' ||
			header[i] >= '0' && header[i] <= '9') {
			i++
		}
		name := strings.ToLower(header[start:i])
		for i < len(header) && (header[i] == ' ' || header[i] == '\t') {
			i++
		}
		if i >= len(header) || header[i] != '=' {
			if i == start {
				i++
			}
			continue
		}
		i++
		for i < len(header) && (header[i] == ' ' || header[i] == '\t') {
			i++
		}
		var value strings.Builder
		if i < len(header) && header[i] == '"' {
			i++
			for i < len(header) {
				if header[i] == '\\' && i+1 < len(header) {
					i++
					value.WriteByte(header[i])
					i++
					continue
				}
				if header[i] == '"' {
					i++
					break
				}
				value.WriteByte(header[i])
				i++
			}
		} else {
			for i < len(header) && header[i] != ',' && header[i] != ' ' && header[i] != '\t' {
				value.WriteByte(header[i])
				i++
			}
		}
		if name == strings.ToLower(wanted) {
			return value.String()
		}
	}
	return ""
}

func probeMCPResourceMetadataURL(ctx context.Context, client *http.Client, endpoint string) string {
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpHTTPProtocolVersion)
	req.Header.Set("User-Agent", "gokin-studio/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
	for _, challenge := range resp.Header.Values("WWW-Authenticate") {
		if metadata := oauthChallengeParameter(challenge, "resource_metadata"); metadata != "" {
			return metadata
		}
	}
	return ""
}

func protectedResourceMetadataCandidates(endpoint string) ([]string, error) {
	parsed, err := validateMCPOAuthURL(endpoint, "MCP resource")
	if err != nil {
		return nil, err
	}
	endpointPath := strings.TrimPrefix(path.Clean("/"+parsed.Path), "/")
	root := *parsed
	root.RawQuery = ""
	root.Path = "/.well-known/oauth-protected-resource"
	root.RawPath = ""
	candidates := []string{}
	if endpointPath != "" && endpointPath != "." {
		withPath := root
		withPath.Path += "/" + endpointPath
		candidates = append(candidates, withPath.String())
	}
	candidates = append(candidates, root.String())
	return candidates, nil
}

func discoverMCPProtectedResource(
	ctx context.Context,
	client *http.Client,
	endpoint string,
) (mcpOAuthProtectedResource, error) {
	resource, err := canonicalMCPResource(endpoint)
	if err != nil {
		return mcpOAuthProtectedResource{}, err
	}
	var candidates []string
	if challenged := probeMCPResourceMetadataURL(ctx, client, endpoint); challenged != "" {
		candidates = append(candidates, challenged)
	}
	fallbacks, err := protectedResourceMetadataCandidates(endpoint)
	if err != nil {
		return mcpOAuthProtectedResource{}, err
	}
	candidates = append(candidates, fallbacks...)
	seen := map[string]bool{}
	var lastErr error
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		if err := validateDiscoveredMCPOAuthURL(endpoint, candidate, "protected-resource metadata"); err != nil {
			return mcpOAuthProtectedResource{}, err
		}
		var metadata mcpOAuthProtectedResource
		if err := fetchMCPOAuthJSON(ctx, client, candidate, &metadata); err != nil {
			lastErr = err
			continue
		}
		if metadata.Resource == "" {
			metadata.Resource = resource
		}
		canonicalMetadataResource, err := canonicalMCPResource(metadata.Resource)
		if err != nil || canonicalMetadataResource != resource {
			return mcpOAuthProtectedResource{}, fmt.Errorf("protected-resource metadata audience does not match the configured MCP URL")
		}
		if len(metadata.AuthorizationServers) == 0 || len(metadata.AuthorizationServers) > 8 {
			return mcpOAuthProtectedResource{}, fmt.Errorf("protected-resource metadata must name 1 to 8 authorization servers")
		}
		for _, authorizationServer := range metadata.AuthorizationServers {
			if err := validateDiscoveredMCPOAuthURL(resource, authorizationServer, "authorization server"); err != nil {
				return mcpOAuthProtectedResource{}, err
			}
		}
		if err := validateOAuthScopes(metadata.ScopesSupported); err != nil {
			return mcpOAuthProtectedResource{}, err
		}
		return metadata, nil
	}
	if lastErr == nil {
		lastErr = errors.New("resource metadata was not advertised")
	}
	return mcpOAuthProtectedResource{}, fmt.Errorf("discover MCP OAuth protected resource: %w", lastErr)
}

func authorizationServerMetadataCandidates(issuer string) ([]string, error) {
	parsed, err := validateMCPOAuthURL(issuer, "authorization server")
	if err != nil {
		return nil, err
	}
	if parsed.RawQuery != "" {
		return nil, fmt.Errorf("authorization server issuer cannot contain a query")
	}
	issuerPath := strings.Trim(parsed.EscapedPath(), "/")
	oauth := *parsed
	oauth.Path = "/.well-known/oauth-authorization-server"
	oauth.RawPath = ""
	if issuerPath != "" {
		oauth.Path += "/" + issuerPath
	}
	oidc := *parsed
	oidc.Path = strings.TrimSuffix(parsed.Path, "/") + "/.well-known/openid-configuration"
	oidc.RawPath = ""
	return []string{oauth.String(), oidc.String()}, nil
}

func discoverMCPAuthorizationServer(
	ctx context.Context,
	client *http.Client,
	issuer string,
) (mcpOAuthAuthorizationServer, error) {
	expected, err := validateMCPOAuthURL(issuer, "authorization server")
	if err != nil {
		return mcpOAuthAuthorizationServer{}, err
	}
	expected.RawQuery = ""
	expected.Fragment = ""
	candidates, err := authorizationServerMetadataCandidates(expected.String())
	if err != nil {
		return mcpOAuthAuthorizationServer{}, err
	}
	var lastErr error
	for _, candidate := range candidates {
		var metadata mcpOAuthAuthorizationServer
		if err := fetchMCPOAuthJSON(ctx, client, candidate, &metadata); err != nil {
			lastErr = err
			continue
		}
		actual, err := validateMCPOAuthURL(metadata.Issuer, "authorization issuer")
		if err != nil {
			return mcpOAuthAuthorizationServer{}, err
		}
		actual.RawQuery = ""
		if actual.String() != expected.String() {
			return mcpOAuthAuthorizationServer{}, fmt.Errorf("authorization metadata issuer mismatch")
		}
		if _, err := validateMCPOAuthURL(metadata.AuthorizationEndpoint, "authorization endpoint"); err != nil {
			return mcpOAuthAuthorizationServer{}, err
		}
		if err := validateDiscoveredMCPOAuthURL(expected.String(), metadata.AuthorizationEndpoint, "authorization endpoint"); err != nil {
			return mcpOAuthAuthorizationServer{}, err
		}
		if _, err := validateMCPOAuthURL(metadata.TokenEndpoint, "token endpoint"); err != nil {
			return mcpOAuthAuthorizationServer{}, err
		}
		if err := validateDiscoveredMCPOAuthURL(expected.String(), metadata.TokenEndpoint, "token endpoint"); err != nil {
			return mcpOAuthAuthorizationServer{}, err
		}
		if metadata.RegistrationEndpoint != "" {
			if _, err := validateMCPOAuthURL(metadata.RegistrationEndpoint, "registration endpoint"); err != nil {
				return mcpOAuthAuthorizationServer{}, err
			}
			if err := validateDiscoveredMCPOAuthURL(expected.String(), metadata.RegistrationEndpoint, "registration endpoint"); err != nil {
				return mcpOAuthAuthorizationServer{}, err
			}
		}
		if !containsOAuthValue(metadata.CodeChallengeMethodsSupported, "S256") {
			return mcpOAuthAuthorizationServer{}, fmt.Errorf("authorization server does not advertise required S256 PKCE support")
		}
		if err := validateOAuthScopes(metadata.ScopesSupported); err != nil {
			return mcpOAuthAuthorizationServer{}, err
		}
		return metadata, nil
	}
	if lastErr == nil {
		lastErr = errors.New("authorization metadata was not found")
	}
	return mcpOAuthAuthorizationServer{}, fmt.Errorf("discover authorization server metadata: %w", lastErr)
}

func containsOAuthValue(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), wanted) {
			return true
		}
	}
	return false
}

func validateOAuthScopes(scopes []string) error {
	if len(scopes) > 64 {
		return fmt.Errorf("OAuth metadata advertises too many scopes")
	}
	total := 0
	for _, scope := range scopes {
		if scope == "" || len(scope) > 256 || strings.ContainsAny(scope, " \t\r\n\x00") {
			return fmt.Errorf("OAuth metadata contains an invalid scope")
		}
		total += len(scope)
	}
	if total > 4096 {
		return fmt.Errorf("OAuth scope metadata is too large")
	}
	return nil
}

func randomOAuthValue(bytesCount int) (string, error) {
	value := make([]byte, bytesCount)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func registerMCPOAuthClient(
	ctx context.Context,
	client *http.Client,
	endpoint, redirectURI string,
) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"client_name":                "Gokin Studio",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"application_type":           "native",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gokin-studio/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	var registered struct {
		ClientID                string `json:"client_id"`
		TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
	}
	if err := readBoundedOAuthJSON(resp, maxMCPOAuthMetadataBytes, &registered); err != nil {
		return "", fmt.Errorf("dynamic client registration: %w", err)
	}
	if registered.ClientID == "" || len(registered.ClientID) > 2048 || strings.ContainsAny(registered.ClientID, "\r\n\x00") {
		return "", fmt.Errorf("dynamic client registration returned an invalid client ID")
	}
	method := strings.TrimSpace(registered.TokenEndpointAuthMethod)
	if method != "" && method != "none" {
		return "", fmt.Errorf("authorization server registered a confidential client; Gokin requires a public PKCE client")
	}
	return registered.ClientID, nil
}

func startMCPOAuthCallback(state string) (
	redirectURI string,
	results <-chan mcpOAuthCallback,
	closeServer func(),
	err error,
) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, err
	}
	callbackToken, err := randomOAuthValue(18)
	if err != nil {
		_ = listener.Close()
		return "", nil, nil, err
	}
	callbackPath := "/oauth/callback/" + callbackToken
	redirectURI = "http://" + listener.Addr().String() + callbackPath
	resultCh := make(chan mcpOAuthCallback, 1)
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if subtleStringMismatch(req.URL.Query().Get("state"), state) {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			return
		}
		result := mcpOAuthCallback{
			Code:  req.URL.Query().Get("code"),
			Error: req.URL.Query().Get("error"),
		}
		if result.Code == "" && result.Error == "" {
			http.Error(w, "Missing OAuth result", http.StatusBadRequest)
			return
		}
		once.Do(func() { resultCh <- result })
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "<!doctype html><meta charset=utf-8><title>Gokin Studio</title><p>Authorization received. You can return to Gokin Studio and close this tab.</p>")
	})
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		_ = server.Serve(listener)
	}()
	closeFn := func() {
		_ = server.Close()
		_ = listener.Close()
	}
	return redirectURI, resultCh, closeFn, nil
}

func subtleStringMismatch(actual, expected string) bool {
	return len(actual) == 0 || subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1
}

func chooseMCPOAuthScopes(resource, authorization []string) []string {
	if len(resource) > 0 {
		return append([]string(nil), resource...)
	}
	// Authorization-server metadata often advertises every scope supported by
	// the identity provider (openid, profile, admin, etc.). Requesting all of
	// them would violate least privilege. When the protected MCP resource does
	// not publish a minimal scope set, omit scope and let the server apply its
	// registered/default policy.
	_ = authorization
	return nil
}

func exchangeMCPOAuthToken(
	ctx context.Context,
	client *http.Client,
	tokenEndpoint string,
	values url.Values,
) (mcpOAuthCredential, error) {
	if _, err := validateMCPOAuthURL(tokenEndpoint, "token endpoint"); err != nil {
		return mcpOAuthCredential{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return mcpOAuthCredential{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gokin-studio/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return mcpOAuthCredential{}, err
	}
	var token struct {
		AccessToken  string          `json:"access_token"`
		RefreshToken string          `json:"refresh_token"`
		TokenType    string          `json:"token_type"`
		Scope        string          `json:"scope"`
		ExpiresIn    json.RawMessage `json:"expires_in"`
	}
	if err := readBoundedOAuthJSON(resp, maxMCPOAuthTokenBytes, &token); err != nil {
		return mcpOAuthCredential{}, err
	}
	if token.AccessToken == "" || len(token.AccessToken) > 48<<10 || strings.ContainsAny(token.AccessToken, "\r\n\x00") {
		return mcpOAuthCredential{}, fmt.Errorf("token endpoint returned an invalid access token")
	}
	if !strings.EqualFold(token.TokenType, "Bearer") {
		return mcpOAuthCredential{}, fmt.Errorf("token endpoint returned unsupported token type %q", token.TokenType)
	}
	if len(token.RefreshToken) > 48<<10 || strings.ContainsAny(token.RefreshToken, "\r\n\x00") {
		return mcpOAuthCredential{}, fmt.Errorf("token endpoint returned an invalid refresh token")
	}
	expiresIn, err := parseOAuthExpiresIn(token.ExpiresIn)
	if err != nil {
		return mcpOAuthCredential{}, err
	}
	expiresAt := int64(0)
	if expiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second).Unix()
	}
	return mcpOAuthCredential{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		Scope:        token.Scope,
		ExpiresAt:    expiresAt,
	}, nil
}

func parseOAuthExpiresIn(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		value, err := strconv.ParseInt(number.String(), 10, 64)
		if err == nil && value >= 0 && value <= int64((365*24*time.Hour)/time.Second) {
			return value, nil
		}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		value, err := strconv.ParseInt(text, 10, 64)
		if err == nil && value >= 0 && value <= int64((365*24*time.Hour)/time.Second) {
			return value, nil
		}
	}
	return 0, fmt.Errorf("token endpoint returned invalid expires_in")
}

func (s *Studio) AuthorizeMCPServer(name string) (*MCPAuthorizationResult, error) {
	name = strings.TrimSpace(name)
	runKey := strings.ToLower(name)
	s.mcpOAuthMu.Lock()
	if s.mcpOAuthRuns == nil {
		s.mcpOAuthRuns = make(map[string]bool)
	}
	if s.mcpOAuthRuns[runKey] {
		s.mcpOAuthMu.Unlock()
		return nil, fmt.Errorf("OAuth authorization is already running for %s", name)
	}
	s.mcpOAuthRuns[runKey] = true
	s.mcpOAuthMu.Unlock()
	defer func() {
		s.mcpOAuthMu.Lock()
		delete(s.mcpOAuthRuns, runKey)
		s.mcpOAuthMu.Unlock()
	}()

	configs, err := loadMCPServers()
	if err != nil {
		return nil, err
	}
	var cfg *MCPServerConfig
	for i := range configs {
		if strings.EqualFold(configs[i].Name, name) {
			copy := configs[i]
			cfg = &copy
			break
		}
	}
	if cfg == nil {
		return nil, fmt.Errorf("MCP server not found: %s", name)
	}
	if cfg.Transport != mcpTransportHTTP || cfg.AuthType != mcpAuthOAuth {
		return nil, fmt.Errorf("connector is not configured for OAuth")
	}
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, mcpOAuthCallbackTimeout)
	defer cancel()
	client := s.testMCPOAuthHTTPClient
	if client == nil {
		client = newMCPOAuthHTTPClient()
	}
	protected, err := discoverMCPProtectedResource(ctx, client, cfg.URL)
	if err != nil {
		return nil, err
	}
	authMetadata, err := discoverMCPAuthorizationServer(ctx, client, protected.AuthorizationServers[0])
	if err != nil {
		return nil, err
	}
	state, err := randomOAuthValue(32)
	if err != nil {
		return nil, err
	}
	verifier, err := randomOAuthValue(48)
	if err != nil {
		return nil, err
	}
	redirectURI, callbacks, closeCallback, err := startMCPOAuthCallback(state)
	if err != nil {
		return nil, fmt.Errorf("start OAuth callback: %w", err)
	}
	defer closeCallback()

	clientID := strings.TrimSpace(cfg.OAuthClientID)
	if clientID == "" {
		if authMetadata.RegistrationEndpoint == "" {
			return nil, fmt.Errorf("authorization server offers no dynamic client registration; configure its public client ID")
		}
		clientID, err = registerMCPOAuthClient(ctx, client, authMetadata.RegistrationEndpoint, redirectURI)
		if err != nil {
			return nil, err
		}
	}
	if len(clientID) > 2048 || strings.ContainsAny(clientID, "\r\n\x00") {
		return nil, fmt.Errorf("OAuth client ID is invalid")
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	scopes := chooseMCPOAuthScopes(protected.ScopesSupported, authMetadata.ScopesSupported)
	authorizationURL, err := url.Parse(authMetadata.AuthorizationEndpoint)
	if err != nil {
		return nil, err
	}
	query := authorizationURL.Query()
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challengeBytes[:]))
	query.Set("code_challenge_method", "S256")
	query.Set("resource", protected.Resource)
	if len(scopes) > 0 {
		query.Set("scope", strings.Join(scopes, " "))
	}
	authorizationURL.RawQuery = query.Encode()

	openBrowser := s.testMCPOAuthOpenBrowser
	if openBrowser == nil {
		openBrowser = func(target string) error {
			if s.ctx == nil {
				return fmt.Errorf("desktop runtime is not ready")
			}
			wailsRuntime.BrowserOpenURL(s.ctx, target)
			return nil
		}
	}
	if err := openBrowser(authorizationURL.String()); err != nil {
		return nil, fmt.Errorf("open authorization browser: %w", err)
	}

	var callback mcpOAuthCallback
	select {
	case callback = <-callbacks:
	case <-ctx.Done():
		return nil, fmt.Errorf("OAuth authorization timed out or was cancelled")
	}
	if callback.Error != "" {
		return nil, fmt.Errorf("authorization server returned %s", callback.Error)
	}
	if callback.Code == "" || len(callback.Code) > 8192 || strings.ContainsAny(callback.Code, "\r\n\x00") {
		return nil, fmt.Errorf("authorization server returned an invalid code")
	}
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {callback.Code},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
		"resource":      {protected.Resource},
	}
	token, err := exchangeMCPOAuthToken(ctx, client, authMetadata.TokenEndpoint, values)
	if err != nil {
		return nil, fmt.Errorf("exchange OAuth authorization code: %w", err)
	}
	token.Version = 1
	token.Resource = protected.Resource
	token.AuthorizationServer = authMetadata.Issuer
	token.ClientID = clientID
	token.TokenEndpoint = authMetadata.TokenEndpoint
	encoded, err := json.Marshal(token)
	if err != nil {
		return nil, err
	}
	saveCredential := s.testMCPOAuthSave
	if saveCredential == nil {
		saveCredential = saveMCPOAuthCredential
	}
	if err := saveCredential(cfg.Name, encoded); err != nil {
		return nil, err
	}
	s.resetProjectsForMCPChange()
	return &MCPAuthorizationResult{
		Authorized:          true,
		AuthorizationServer: token.AuthorizationServer,
		Scopes:              scopes,
		ExpiresAt:           token.ExpiresAt,
	}, nil
}

func loadMCPOAuthToken(name string) (mcpOAuthCredential, error) {
	data, err := loadMCPOAuthCredential(name)
	if err != nil {
		return mcpOAuthCredential{}, err
	}
	var token mcpOAuthCredential
	if err := json.Unmarshal(data, &token); err != nil {
		return mcpOAuthCredential{}, fmt.Errorf("decode stored OAuth credential: %w", err)
	}
	if token.Version != 1 || token.AccessToken == "" {
		return mcpOAuthCredential{}, fmt.Errorf("stored OAuth credential is invalid")
	}
	return token, nil
}

func mcpOAuthAccessToken(ctx context.Context, cfg MCPServerConfig) (string, error) {
	mcpOAuthRefreshMu.Lock()
	defer mcpOAuthRefreshMu.Unlock()
	token, err := loadMCPOAuthToken(cfg.Name)
	if err != nil {
		if errors.Is(err, errMCPOAuthCredentialNotFound) {
			return "", fmt.Errorf("connector is not authorized; use Connect account in Settings")
		}
		return "", err
	}
	return resolveMCPOAuthAccessToken(ctx, newMCPOAuthHTTPClient(), cfg, token, saveMCPOAuthCredential)
}

func resolveMCPOAuthAccessToken(
	ctx context.Context,
	client *http.Client,
	cfg MCPServerConfig,
	token mcpOAuthCredential,
	save func(string, []byte) error,
) (string, error) {
	resource, err := canonicalMCPResource(cfg.URL)
	if err != nil {
		return "", err
	}
	if token.Resource != resource {
		return "", fmt.Errorf("stored OAuth token is bound to a different MCP resource; reconnect the account")
	}
	if cfg.OAuthClientID != "" && token.ClientID != cfg.OAuthClientID {
		return "", fmt.Errorf("OAuth client ID changed; reconnect the account")
	}
	if token.ExpiresAt == 0 || time.Now().Add(60*time.Second).Unix() < token.ExpiresAt {
		return token.AccessToken, nil
	}
	if token.RefreshToken == "" {
		return "", fmt.Errorf("OAuth access token expired and no refresh token is available; reconnect the account")
	}
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {token.RefreshToken},
		"client_id":     {token.ClientID},
		"resource":      {token.Resource},
	}
	refreshed, err := exchangeMCPOAuthToken(ctx, client, token.TokenEndpoint, values)
	if err != nil {
		return "", fmt.Errorf("refresh OAuth token: %w", err)
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = token.RefreshToken
	}
	refreshed.Version = token.Version
	refreshed.Resource = token.Resource
	refreshed.AuthorizationServer = token.AuthorizationServer
	refreshed.ClientID = token.ClientID
	refreshed.TokenEndpoint = token.TokenEndpoint
	encoded, err := json.Marshal(refreshed)
	if err != nil {
		return "", err
	}
	if err := save(cfg.Name, encoded); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

func (s *Studio) DisconnectMCPServerOAuth(name string) error {
	if err := deleteMCPOAuthCredential(name); err != nil {
		return err
	}
	s.resetProjectsForMCPChange()
	return nil
}

func mcpOAuthAuthorizationStatus(name, resource string) (bool, int64, string) {
	token, err := loadMCPOAuthToken(name)
	if errors.Is(err, errMCPOAuthCredentialNotFound) {
		return false, 0, ""
	}
	if err != nil {
		return false, 0, err.Error()
	}
	canonical, err := canonicalMCPResource(resource)
	if err != nil || token.Resource != canonical {
		return false, 0, "stored OAuth token is bound to a different MCP resource"
	}
	return true, token.ExpiresAt, ""
}

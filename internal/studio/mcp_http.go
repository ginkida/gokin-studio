package studio

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

var mcpHTTPHeaderNameRE = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

var reservedMCPHTTPHeaders = map[string]bool{
	"accept":               true,
	"connection":           true,
	"content-length":       true,
	"content-type":         true,
	"host":                 true,
	"mcp-method":           true,
	"mcp-name":             true,
	"mcp-protocol-version": true,
	"mcp-session-id":       true,
	"origin":               true,
	"transfer-encoding":    true,
}

func validateMCPHTTPConfig(cfg MCPServerConfig) (MCPServerConfig, error) {
	cfg.URL = strings.TrimSpace(cfg.URL)
	if cfg.URL == "" {
		return cfg, fmt.Errorf("remote MCP URL cannot be empty")
	}
	if len(cfg.URL) > 4096 || strings.ContainsRune(cfg.URL, 0) {
		return cfg, fmt.Errorf("invalid remote MCP URL")
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return cfg, fmt.Errorf("remote MCP URL must be absolute")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return cfg, fmt.Errorf("remote MCP URL cannot contain credentials or a fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return cfg, fmt.Errorf("plain HTTP is allowed only for localhost/loopback MCP servers")
		}
	default:
		return cfg, fmt.Errorf("remote MCP URL must use HTTPS (or HTTP on localhost)")
	}
	for key := range parsed.Query() {
		switch strings.ToLower(key) {
		case "access_token", "token", "api_key", "apikey", "authorization":
			return cfg, fmt.Errorf("put credentials in an Authorization header, not the URL query")
		}
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	if len(cfg.Headers) > maxMCPHeaders {
		return cfg, fmt.Errorf("remote MCP server may have at most %d headers", maxMCPHeaders)
	}
	normalized := make(map[string]string, len(cfg.Headers))
	for key, value := range cfg.Headers {
		key = strings.TrimSpace(key)
		lower := strings.ToLower(key)
		if !mcpHTTPHeaderNameRE.MatchString(key) || reservedMCPHTTPHeaders[lower] ||
			strings.HasPrefix(lower, "mcp-param-") {
			return cfg, fmt.Errorf("invalid or reserved HTTP header %q", key)
		}
		if len(value) > maxMCPHeaderValueBytes || strings.ContainsAny(value, "\r\n\x00") {
			return cfg, fmt.Errorf("invalid value for HTTP header %q", key)
		}
		canonical := http.CanonicalHeaderKey(key)
		if _, exists := normalized[canonical]; exists {
			return cfg, fmt.Errorf("duplicate HTTP header %q", key)
		}
		normalized[canonical] = value
	}
	cfg.Headers = normalized
	cfg.AuthType = strings.ToLower(strings.TrimSpace(cfg.AuthType))
	if cfg.AuthType == "" {
		cfg.AuthType = mcpAuthHeaders
	}
	switch cfg.AuthType {
	case mcpAuthHeaders:
		cfg.OAuthClientID = ""
	case mcpAuthOAuth:
		cfg.OAuthClientID = strings.TrimSpace(cfg.OAuthClientID)
		if len(cfg.OAuthClientID) > 2048 || strings.ContainsAny(cfg.OAuthClientID, "\r\n\x00") {
			return cfg, fmt.Errorf("invalid OAuth client ID")
		}
		for key := range normalized {
			if strings.EqualFold(key, "Authorization") {
				return cfg, fmt.Errorf("OAuth connectors cannot also define an Authorization header")
			}
		}
	default:
		return cfg, fmt.Errorf("remote MCP auth type must be headers or oauth")
	}
	cfg.Command = ""
	cfg.Args = nil
	cfg.Env = nil
	return cfg, nil
}

type mcpHTTPError struct {
	Status  int
	Code    int
	Message string
}

func (e *mcpHTTPError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = http.StatusText(e.Status)
	}
	if e.Code != 0 {
		return fmt.Sprintf("remote MCP HTTP %d (JSON-RPC %d): %s", e.Status, e.Code, message)
	}
	return fmt.Sprintf("remote MCP HTTP %d: %s", e.Status, message)
}

type mcpHTTPHeaderBinding struct {
	Path   []string
	Header string
	Type   string
}

type mcpHTTPTransport struct {
	cfg             MCPServerConfig
	endpoint        *url.URL
	client          *http.Client
	transport       *http.Transport
	nextID          atomic.Int64
	mu              sync.RWMutex
	protocolVersion string
	modern          bool
	sessionID       string
	toolHeaders     map[string][]mcpHTTPHeaderBinding
	closeOnce       sync.Once
}

func connectMCPHTTP(ctx context.Context, cfg MCPServerConfig) (*mcpClient, error) {
	if cfg.AuthType == mcpAuthOAuth {
		token, err := mcpOAuthAccessToken(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("authorize %s: %w", cfg.Name, err)
		}
		headers := make(map[string]string, len(cfg.Headers)+1)
		for key, value := range cfg.Headers {
			headers[key] = value
		}
		headers["Authorization"] = "Bearer " + token
		cfg.Headers = headers
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: mcpToolCallTimeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	h := &mcpHTTPTransport{
		cfg: cfg, endpoint: parsed, transport: transport,
		protocolVersion: mcpHTTPProtocolVersion, modern: true,
		toolHeaders: make(map[string][]mcpHTTPHeaderBinding),
	}
	h.client = &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Never forward connector credentials to a redirected endpoint.
			return errors.New("remote MCP redirects are disabled; configure the final endpoint URL")
		},
	}
	c := &mcpClient{cfg: cfg, http: h}

	// The 2026 revision is stateless: tools/list is a valid first request.
	err = c.discoverTools(ctx)
	if err == nil {
		h.installToolHeaderBindings(c)
		return c, nil
	}
	if !shouldFallbackMCPLegacy(err) {
		_ = h.Close()
		return nil, fmt.Errorf("connect %s: %w", cfg.Name, err)
	}

	// Widely deployed 2025 servers require initialize + optional session ID.
	c.tools = nil
	h.modern = false
	h.protocolVersion = mcpHTTPLegacyVersion
	if err := h.initializeLegacy(ctx, c); err != nil {
		var httpErr *mcpHTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusBadRequest {
			h.protocolVersion = mcpHTTPLegacyFallback
			h.sessionID = ""
			err = h.initializeLegacy(ctx, c)
		}
		if err != nil {
			_ = h.Close()
			return nil, fmt.Errorf("connect %s: %w", cfg.Name, err)
		}
	}
	h.installToolHeaderBindings(c)
	return c, nil
}

func shouldFallbackMCPLegacy(err error) bool {
	var httpErr *mcpHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status == http.StatusBadRequest ||
			httpErr.Status == http.StatusNotFound ||
			httpErr.Status == http.StatusMethodNotAllowed
	}
	var rpcErr *mcpRemoteCallError
	if errors.As(err, &rpcErr) {
		message := strings.ToLower(rpcErr.message)
		return strings.Contains(message, "initialize") || rpcErr.code == -32002
	}
	return false
}

func (h *mcpHTTPTransport) initializeLegacy(ctx context.Context, c *mcpClient) error {
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := h.request(ctx, "initialize", map[string]any{
		"protocolVersion": h.protocolVersion,
		"capabilities":    mcpAppCapabilities(),
		"clientInfo":      map[string]any{"name": "gokin-studio", "version": Version},
	}, &result); err != nil {
		return fmt.Errorf("initialize remote MCP: %w", err)
	}
	switch result.ProtocolVersion {
	case mcpHTTPLegacyVersion, mcpHTTPLegacyFallback:
		h.protocolVersion = result.ProtocolVersion
	case "":
		// Some early servers omitted the echo; retain the requested version.
	default:
		return fmt.Errorf("remote MCP negotiated unsupported protocol version %q", result.ProtocolVersion)
	}
	if err := h.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return err
	}
	return c.discoverTools(ctx)
}

func (h *mcpHTTPTransport) request(ctx context.Context, method string, params any, out any) error {
	id := h.nextID.Add(1)
	return h.post(ctx, method, params, &id, out)
}

func (h *mcpHTTPTransport) notify(ctx context.Context, method string, params any) error {
	return h.post(ctx, method, params, nil, nil)
}

func (h *mcpHTTPTransport) post(ctx context.Context, method string, params any, id *int64, out any) error {
	bodyParams, err := h.paramsWithMetadata(params)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  bodyParams,
	}
	if id != nil {
		payload["id"] = *id
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	for key, value := range h.cfg.Headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "gokin-studio/"+Version)
	req.Header.Set("MCP-Protocol-Version", h.protocolVersion)
	req.Header.Set("Mcp-Method", method)
	if name := mcpRequestName(method, bodyParams); name != "" {
		req.Header.Set("Mcp-Name", encodeMCPHTTPHeaderValue(name))
	}
	h.mu.RLock()
	sessionID := h.sessionID
	h.mu.RUnlock()
	if !h.modern && sessionID != "" && method != "initialize" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if method == "tools/call" {
		if err := h.addToolParameterHeaders(req.Header, bodyParams); err != nil {
			return err
		}
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxMCPHTTPResponseBytes+1))
	if readErr != nil {
		return readErr
	}
	if len(data) > maxMCPHTTPResponseBytes {
		return fmt.Errorf("remote MCP response exceeds the 8 MiB limit")
	}
	if session := resp.Header.Get("Mcp-Session-Id"); session != "" {
		if !visibleASCII(session) {
			return fmt.Errorf("remote MCP returned an invalid session ID")
		}
		h.mu.Lock()
		h.sessionID = session
		h.mu.Unlock()
	}
	if id == nil && resp.StatusCode == http.StatusAccepted {
		return nil
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rpcCode, message := parseMCPHTTPErrorBody(data, contentType)
		if resp.StatusCode == http.StatusUnauthorized && message == "" {
			message = "authorization required; connect the OAuth account or configure a Bearer token header"
		} else if resp.StatusCode == http.StatusForbidden && message == "" {
			message = "connector credentials lack the required scope"
		}
		return &mcpHTTPError{Status: resp.StatusCode, Code: rpcCode, Message: message}
	}
	if id == nil {
		return nil
	}
	return decodeMCPHTTPResponse(data, contentType, *id, out)
}

func (h *mcpHTTPTransport) paramsWithMetadata(params any) (map[string]any, error) {
	result := map[string]any{}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("MCP params must be an object: %w", err)
		}
	}
	if h.modern {
		meta := map[string]any{}
		if existing, ok := result["_meta"].(map[string]any); ok {
			for key, value := range existing {
				meta[key] = value
			}
		}
		meta["io.modelcontextprotocol/protocolVersion"] = h.protocolVersion
		meta["io.modelcontextprotocol/clientInfo"] = map[string]any{
			"name": "gokin-studio", "version": Version,
		}
		meta["io.modelcontextprotocol/clientCapabilities"] = mcpAppCapabilities()
		result["_meta"] = meta
	}
	return result, nil
}

func mcpRequestName(method string, params map[string]any) string {
	switch method {
	case "tools/call", "prompts/get":
		value, _ := params["name"].(string)
		return value
	case "resources/read":
		value, _ := params["uri"].(string)
		return value
	default:
		return ""
	}
}

func decodeMCPHTTPResponse(data []byte, contentType string, id int64, out any) error {
	if strings.Contains(contentType, "text/event-stream") {
		for _, eventData := range parseSSEData(data) {
			matched, err := decodeMCPJSONRPC(eventData, id, out)
			if matched || err != nil {
				return err
			}
		}
		return io.ErrUnexpectedEOF
	}
	matched, err := decodeMCPJSONRPC(data, id, out)
	if err != nil {
		return err
	}
	if !matched {
		return fmt.Errorf("remote MCP response ID did not match request")
	}
	return nil
}

func decodeMCPJSONRPC(data []byte, id int64, out any) (bool, error) {
	var response mcpResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return false, err
	}
	var responseID int64
	if len(response.ID) == 0 || json.Unmarshal(response.ID, &responseID) != nil || responseID != id {
		return false, nil
	}
	if response.Error != nil {
		return true, &mcpRemoteCallError{code: response.Error.Code, message: response.Error.Message}
	}
	if out != nil {
		return true, json.Unmarshal(response.Result, out)
	}
	return true, nil
}

func parseSSEData(data []byte) [][]byte {
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	var events [][]byte
	for _, block := range strings.Split(normalized, "\n\n") {
		var lines []string
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "data:") {
				lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if len(lines) > 0 {
			events = append(events, []byte(strings.Join(lines, "\n")))
		}
	}
	return events
}

func parseMCPHTTPErrorBody(data []byte, contentType string) (int, string) {
	var candidates [][]byte
	if strings.Contains(contentType, "text/event-stream") {
		candidates = parseSSEData(data)
	} else {
		candidates = [][]byte{data}
	}
	for _, candidate := range candidates {
		var response mcpResponse
		if json.Unmarshal(candidate, &response) == nil && response.Error != nil {
			return response.Error.Code, truncateMCPRunes(strings.TrimSpace(response.Error.Message), 500)
		}
	}
	return 0, ""
}

func visibleASCII(value string) bool {
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return value != ""
}

func encodeMCPHTTPHeaderValue(value string) string {
	safe := value != "" && strings.TrimSpace(value) == value &&
		!strings.HasPrefix(value, "=?base64?") && !strings.HasSuffix(value, "?=")
	if safe {
		for _, r := range value {
			if r < 0x20 || r > 0x7e {
				safe = false
				break
			}
		}
	}
	if safe {
		return value
	}
	return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(value)) + "?="
}

func (h *mcpHTTPTransport) installToolHeaderBindings(c *mcpClient) {
	filtered := c.tools[:0]
	for _, tool := range c.tools {
		bindings, err := extractMCPHTTPHeaderBindings(tool.InputSchema)
		if err != nil {
			// 2026 requires malformed header annotations to exclude that tool;
			// keep the rest of the connector usable.
			continue
		}
		h.toolHeaders[tool.Name] = bindings
		filtered = append(filtered, tool)
	}
	c.tools = filtered
}

func extractMCPHTTPHeaderBindings(schema map[string]any) ([]mcpHTTPHeaderBinding, error) {
	var out []mcpHTTPHeaderBinding
	seen := map[string]bool{}
	var walk func(map[string]any, []string, bool) error
	walk = func(node map[string]any, path []string, reachable bool) error {
		if raw, exists := node["x-mcp-header"]; exists {
			name, ok := raw.(string)
			if !ok || name == "" || !mcpHTTPHeaderNameRE.MatchString(name) {
				return fmt.Errorf("invalid x-mcp-header annotation")
			}
			if !reachable || len(path) == 0 {
				return fmt.Errorf("x-mcp-header is not on a statically reachable property")
			}
			kind, _ := node["type"].(string)
			if kind != "string" && kind != "integer" && kind != "boolean" {
				return fmt.Errorf("x-mcp-header property must be string, integer, or boolean")
			}
			lower := strings.ToLower(name)
			if seen[lower] || reservedMCPHTTPHeaders["mcp-param-"+lower] {
				return fmt.Errorf("duplicate or reserved x-mcp-header %q", name)
			}
			seen[lower] = true
			out = append(out, mcpHTTPHeaderBinding{
				Path: append([]string(nil), path...), Header: name, Type: kind,
			})
		}
		for key, raw := range node {
			if key == "x-mcp-header" {
				continue
			}
			if key == "properties" {
				properties, _ := raw.(map[string]any)
				for name, childRaw := range properties {
					if child, ok := childRaw.(map[string]any); ok {
						if err := walk(child, append(path, name), reachable); err != nil {
							return err
						}
					}
				}
				continue
			}
			switch child := raw.(type) {
			case map[string]any:
				if err := walk(child, path, false); err != nil {
					return err
				}
			case []any:
				for _, item := range child {
					if childMap, ok := item.(map[string]any); ok {
						if err := walk(childMap, path, false); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	}
	if err := walk(schema, nil, true); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *mcpHTTPTransport) addToolParameterHeaders(headers http.Header, params map[string]any) error {
	name, _ := params["name"].(string)
	args, _ := params["arguments"].(map[string]any)
	for _, binding := range h.toolHeaders[name] {
		var value any = args
		present := true
		for _, segment := range binding.Path {
			object, ok := value.(map[string]any)
			if !ok {
				present = false
				break
			}
			value, present = object[segment]
			if !present {
				break
			}
		}
		if !present || value == nil {
			continue
		}
		encoded, err := mcpHeaderPrimitive(value, binding.Type)
		if err != nil {
			return fmt.Errorf("tool %s parameter %s: %w", name, strings.Join(binding.Path, "."), err)
		}
		headers.Set("Mcp-Param-"+binding.Header, encodeMCPHTTPHeaderValue(encoded))
	}
	return nil
}

func mcpHeaderPrimitive(value any, kind string) (string, error) {
	switch kind {
	case "string":
		text, ok := value.(string)
		if !ok || !utf8.ValidString(text) {
			return "", fmt.Errorf("expected a UTF-8 string")
		}
		return text, nil
	case "boolean":
		flag, ok := value.(bool)
		if !ok {
			return "", fmt.Errorf("expected a boolean")
		}
		return strconv.FormatBool(flag), nil
	case "integer":
		switch number := value.(type) {
		case int:
			return strconv.Itoa(number), nil
		case int64:
			if number < -(1<<53)+1 || number > (1<<53)-1 {
				return "", fmt.Errorf("integer exceeds the safe JSON range")
			}
			return strconv.FormatInt(number, 10), nil
		case float64:
			if math.Trunc(number) != number || number < -(1<<53)+1 || number > (1<<53)-1 {
				return "", fmt.Errorf("expected a safe integer")
			}
			return strconv.FormatInt(int64(number), 10), nil
		default:
			return "", fmt.Errorf("expected an integer")
		}
	default:
		return "", fmt.Errorf("unsupported header parameter type")
	}
}

func (h *mcpHTTPTransport) Close() error {
	h.closeOnce.Do(func() {
		h.mu.RLock()
		sessionID := h.sessionID
		h.mu.RUnlock()
		if !h.modern && sessionID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			req, err := http.NewRequestWithContext(ctx, http.MethodDelete, h.endpoint.String(), nil)
			if err == nil {
				for key, value := range h.cfg.Headers {
					req.Header.Set(key, value)
				}
				req.Header.Set("MCP-Protocol-Version", h.protocolVersion)
				req.Header.Set("Mcp-Session-Id", sessionID)
				if resp, requestErr := h.client.Do(req); requestErr == nil {
					_ = resp.Body.Close()
				}
			}
			cancel()
		}
		h.transport.CloseIdleConnections()
	})
	return nil
}

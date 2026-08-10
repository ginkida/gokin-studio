package studio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

const (
	// stdio servers continue using the broadly deployed stateful revision.
	mcpProtocolVersion      = "2025-03-26"
	mcpHTTPProtocolVersion  = "2026-07-28"
	mcpHTTPLegacyVersion    = "2025-06-18"
	mcpHTTPLegacyFallback   = "2025-03-26"
	mcpTransportStdio       = "stdio"
	mcpTransportHTTP        = "http"
	maxMCPHeaders           = 32
	maxMCPHeaderValueBytes  = 16 << 10
	maxMCPHTTPResponseBytes = 8 << 20
)

const mcpToolCallTimeout = 90 * time.Second

const maxMCPDiagnosticBytes = 64 << 10

const (
	maxMCPMessageBytes         = 2 << 20
	maxMCPTools                = 512
	maxMCPToolPages            = 32
	maxMCPConfigBytes          = 1 << 20
	maxMCPServers              = 32
	maxMCPArgs                 = 64
	maxMCPArgBytes             = 4096
	maxMCPEnvVars              = 64
	maxMCPEnvValueBytes        = 16 << 10
	maxMCPToolDeclarationBytes = 64 << 10
	maxMCPDeclarationsBytes    = 4 << 20
	maxMCPRemoteToolNameRunes  = 128
	maxMCPDescriptionRunes     = 2000
	maxMCPSchemaDepth          = 16
	maxMCPSchemaNodes          = 2048
)

// MCPServerConfig supports local stdio and remote Streamable HTTP connectors.
// stdio commands execute directly (never through a shell). HTTP headers are
// stored in the 0600 connector file and are intended for bearer/API tokens;
// access tokens are never accepted in URL query parameters.
type MCPServerConfig struct {
	Name          string            `json:"name"`
	Transport     string            `json:"transport,omitempty"` // stdio | http; empty migrates to stdio
	Command       string            `json:"command,omitempty"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	URL           string            `json:"url,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	AuthType      string            `json:"authType,omitempty"` // headers | oauth
	OAuthClientID string            `json:"oauthClientID,omitempty"`
	Enabled       bool              `json:"enabled"`
}

type MCPServerStatus struct {
	MCPServerConfig
	ToolCount              int    `json:"toolCount"`
	Error                  string `json:"error,omitempty"`
	Authorized             bool   `json:"authorized,omitempty"`
	AuthorizationExpiresAt int64  `json:"authorizationExpiresAt,omitempty"`
	AuthorizationError     string `json:"authorizationError,omitempty"`
}

func mcpConfigPath() string { return filepath.Join(configDir(), "mcp_servers.json") }

func loadMCPServersRaw() ([]MCPServerConfig, error) {
	f, err := os.Open(mcpConfigPath())
	if os.IsNotExist(err) {
		return []MCPServerConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxMCPConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMCPConfigBytes {
		return nil, fmt.Errorf("MCP config exceeds the 1 MB limit")
	}
	var configs []MCPServerConfig
	if err := json.Unmarshal(data, &configs); err != nil {
		return nil, fmt.Errorf("parse MCP config: %w", err)
	}
	return configs, nil
}

func loadMCPServers() ([]MCPServerConfig, error) {
	configs, err := loadMCPServersRaw()
	if err != nil {
		return nil, err
	}
	return validateMCPConfigs(configs)
}

func saveMCPServers(configs []MCPServerConfig) error {
	configs, err := validateMCPConfigs(configs)
	if err != nil {
		return err
	}
	return saveMCPServersRaw(configs)
}

func saveMCPServersRaw(configs []MCPServerConfig) error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxMCPConfigBytes {
		return fmt.Errorf("MCP config exceeds the 1 MB limit")
	}
	return atomicWriteFile(mcpConfigPath(), data, 0o600)
}

var mcpEnvKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var mcpConfigMu sync.Mutex

func validateMCPConfig(cfg MCPServerConfig) (MCPServerConfig, error) {
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.Transport = strings.ToLower(strings.TrimSpace(cfg.Transport))
	if cfg.Transport == "" {
		cfg.Transport = mcpTransportStdio
	}
	cfg.Command = strings.TrimSpace(cfg.Command)
	if cfg.Name == "" {
		return cfg, fmt.Errorf("server name cannot be empty")
	}
	if len([]rune(cfg.Name)) > 50 {
		return cfg, fmt.Errorf("server name must be at most 50 characters")
	}
	switch cfg.Transport {
	case mcpTransportStdio:
		if cfg.Command == "" {
			return cfg, fmt.Errorf("command cannot be empty")
		}
		if len(cfg.Command) > maxMCPArgBytes || strings.ContainsRune(cfg.Command, 0) {
			return cfg, fmt.Errorf("invalid command")
		}
		if len(cfg.Args) > maxMCPArgs {
			return cfg, fmt.Errorf("server may have at most %d arguments", maxMCPArgs)
		}
		for _, arg := range cfg.Args {
			if len(arg) > maxMCPArgBytes || strings.ContainsRune(arg, 0) {
				return cfg, fmt.Errorf("invalid command argument")
			}
		}
		if cfg.Env == nil {
			cfg.Env = map[string]string{}
		}
		if len(cfg.Env) > maxMCPEnvVars {
			return cfg, fmt.Errorf("server may have at most %d environment variables", maxMCPEnvVars)
		}
		for key, value := range cfg.Env {
			if !mcpEnvKeyRE.MatchString(key) || len(value) > maxMCPEnvValueBytes || strings.ContainsRune(value, 0) {
				return cfg, fmt.Errorf("invalid environment variable")
			}
		}
		cfg.URL = ""
		cfg.Headers = nil
		cfg.AuthType = ""
		cfg.OAuthClientID = ""
		return cfg, nil
	case mcpTransportHTTP:
		return validateMCPHTTPConfig(cfg)
	default:
		return cfg, fmt.Errorf("transport must be stdio or http")
	}
}

func validateMCPConfigs(configs []MCPServerConfig) ([]MCPServerConfig, error) {
	if len(configs) > maxMCPServers {
		return nil, fmt.Errorf("MCP config may contain at most %d servers", maxMCPServers)
	}
	normalized := make([]MCPServerConfig, len(configs))
	seen := make(map[string]bool, len(configs))
	for i, cfg := range configs {
		validated, err := validateMCPConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("MCP server %d: %w", i+1, err)
		}
		key := strings.ToLower(validated.Name)
		if seen[key] {
			return nil, fmt.Errorf("duplicate MCP server name: %s", validated.Name)
		}
		seen[key] = true
		normalized[i] = validated
	}
	return normalized, nil
}

// SaveMCPServer adds or replaces a connector and invalidates project clients
// so its tools are discovered on the next turn.
func (s *Studio) SaveMCPServer(cfg MCPServerConfig) error {
	cfg, err := validateMCPConfig(cfg)
	if err != nil {
		return err
	}
	mcpConfigMu.Lock()
	defer mcpConfigMu.Unlock()
	configs, err := loadMCPServersRaw()
	if err != nil {
		return err
	}
	out := make([]MCPServerConfig, 0, len(configs)+1)
	deleteOAuthCredential := false
	for i := range configs {
		if strings.EqualFold(strings.TrimSpace(configs[i].Name), cfg.Name) {
			if previous, previousErr := validateMCPConfig(configs[i]); previousErr == nil &&
				previous.Transport == mcpTransportHTTP && previous.AuthType == mcpAuthOAuth &&
				(cfg.Transport != mcpTransportHTTP || cfg.AuthType != mcpAuthOAuth ||
					previous.URL != cfg.URL || previous.OAuthClientID != cfg.OAuthClientID) {
				deleteOAuthCredential = true
			}
			continue
		}
		out = append(out, configs[i])
	}
	configs = append(out, cfg)
	if len(configs) > maxMCPServers {
		return fmt.Errorf("MCP config may contain at most %d servers", maxMCPServers)
	}
	sort.Slice(configs, func(i, j int) bool { return strings.ToLower(configs[i].Name) < strings.ToLower(configs[j].Name) })
	if err := saveMCPServersRaw(configs); err != nil {
		return err
	}
	if deleteOAuthCredential {
		if err := deleteMCPOAuthCredential(cfg.Name); err != nil {
			return fmt.Errorf("connector saved but old OAuth credential could not be removed: %w", err)
		}
	}
	s.resetProjectsForMCPChange()
	return nil
}

func (s *Studio) RemoveMCPServer(name string) error {
	mcpConfigMu.Lock()
	defer mcpConfigMu.Unlock()
	configs, err := loadMCPServersRaw()
	if err != nil {
		return err
	}
	out := configs[:0]
	found := false
	removedOAuth := false
	name = strings.TrimSpace(name)
	for _, cfg := range configs {
		if strings.EqualFold(strings.TrimSpace(cfg.Name), name) {
			found = true
			if validated, validationErr := validateMCPConfig(cfg); validationErr == nil &&
				validated.Transport == mcpTransportHTTP && validated.AuthType == mcpAuthOAuth {
				removedOAuth = true
			}
			continue
		}
		out = append(out, cfg)
	}
	if !found {
		return fmt.Errorf("MCP server not found: %s", name)
	}
	if removedOAuth {
		if err := deleteMCPOAuthCredential(name); err != nil {
			return fmt.Errorf("remove OAuth credential: %w", err)
		}
	}
	if err := saveMCPServersRaw(out); err != nil {
		return err
	}
	s.resetProjectsForMCPChange()
	return nil
}

func (s *Studio) resetProjectsForMCPChange() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.projects {
		p.mu.Lock()
		p.resetClientLocked()
		p.mu.Unlock()
	}
}

func (s *Studio) ListMCPServers() ([]MCPServerStatus, error) {
	configs, err := loadMCPServersRaw()
	if err != nil {
		return nil, err
	}
	out := make([]MCPServerStatus, len(configs))
	byName := make(map[string][]int, len(configs))
	for i, cfg := range configs {
		validated, validationErr := validateMCPConfig(cfg)
		if validationErr != nil {
			cfg.Enabled = false // invalid entries are visible but never presented as active
			out[i] = MCPServerStatus{MCPServerConfig: cfg, Error: validationErr.Error()}
		} else {
			out[i] = MCPServerStatus{MCPServerConfig: validated}
			if validated.Transport == mcpTransportHTTP && validated.AuthType == mcpAuthOAuth {
				out[i].Authorized, out[i].AuthorizationExpiresAt, out[i].AuthorizationError =
					mcpOAuthAuthorizationStatus(validated.Name, validated.URL)
			}
		}
		key := strings.ToLower(strings.TrimSpace(cfg.Name))
		byName[key] = append(byName[key], i)
	}
	for _, indexes := range byName {
		if len(indexes) < 2 {
			continue
		}
		for _, i := range indexes {
			out[i].Enabled = false
			if out[i].Error != "" {
				out[i].Error += "; "
			}
			out[i].Error += "duplicate server name"
		}
	}
	if len(configs) > maxMCPServers {
		for i := range out {
			out[i].Enabled = false
			if out[i].Error != "" {
				out[i].Error += "; "
			}
			out[i].Error += fmt.Sprintf("config exceeds the %d-server limit", maxMCPServers)
		}
	}
	return out, nil
}

// TestMCPServer starts a temporary stdio/HTTP connection, performs protocol
// negotiation and tool discovery, then closes it.
func (s *Studio) TestMCPServer(cfg MCPServerConfig) (MCPServerStatus, error) {
	cfg, err := validateMCPConfig(cfg)
	if err != nil {
		return MCPServerStatus{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	client, err := connectMCP(ctx, cfg)
	if err != nil {
		return MCPServerStatus{MCPServerConfig: cfg, Error: err.Error()}, err
	}
	defer client.Close()
	return MCPServerStatus{MCPServerConfig: cfg, ToolCount: len(client.tools)}, nil
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpRemoteCallError struct {
	code    int
	message string
}

func (e *mcpRemoteCallError) Error() string {
	return fmt.Sprintf("MCP %d: %s", e.code, e.message)
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

type mcpRemoteTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

// boundedDiagnosticBuffer is an io.Writer for child-process stderr that keeps
// only the most recent diagnostics. MCP servers are third-party processes and
// can be arbitrarily noisy; an unbounded bytes.Buffer lets one consume all of
// the desktop app's memory. The mutex also makes request-side error reporting
// safe while os/exec is still copying stderr concurrently.
type boundedDiagnosticBuffer struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *boundedDiagnosticBuffer) Write(p []byte) (int, error) {
	written := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) >= maxMCPDiagnosticBytes {
		b.data = append(b.data[:0], p[len(p)-maxMCPDiagnosticBytes:]...)
		b.truncated = true
		return written, nil
	}
	if overflow := len(b.data) + len(p) - maxMCPDiagnosticBytes; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return written, nil
}

func (b *boundedDiagnosticBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := string(b.data)
	if b.truncated {
		return "[earlier MCP diagnostics truncated]\n" + text
	}
	return text
}

type mcpClient struct {
	cfg       MCPServerConfig
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	scanner   *bufio.Scanner
	stderr    boundedDiagnosticBuffer
	mu        sync.Mutex
	closeOnce sync.Once
	nextID    atomic.Int64
	tools     []mcpRemoteTool
	appTools  map[string]mcpRemoteTool
	http      *mcpHTTPTransport
	onClose   func()
}

func connectMCP(ctx context.Context, cfg MCPServerConfig) (*mcpClient, error) {
	var err error
	cfg, err = resolveMCPConfigEnvironment(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Transport == mcpTransportHTTP {
		return connectMCPHTTP(ctx, cfg)
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = os.Environ()
	for key, value := range cfg.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c := &mcpClient{cfg: cfg, cmd: cmd, stdin: stdin}
	cmd.Stderr = &c.stderr
	c.scanner = bufio.NewScanner(stdout)
	c.scanner.Buffer(make([]byte, 64*1024), maxMCPMessageBytes)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cfg.Name, err)
	}
	initDone := make(chan error, 1)
	go func() { initDone <- c.initialize(ctx) }()
	select {
	case err := <-initDone:
		if err == nil {
			return c, nil
		}
		_ = c.Close()
		return nil, err
	case <-ctx.Done():
		_ = c.Close()
		return nil, fmt.Errorf("connect %s: %w", cfg.Name, ctx.Err())
	}
}

func (c *mcpClient) initialize(ctx context.Context) error {
	var initResult map[string]any
	if err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    mcpAppCapabilities(),
		"clientInfo":      map[string]any{"name": "gokin-studio", "version": Version},
	}, &initResult); err != nil {
		return fmt.Errorf("initialize %s: %w", c.cfg.Name, err)
	}
	if err := c.notify("notifications/initialized", map[string]any{}); err != nil {
		return err
	}
	return c.discoverTools(ctx)
}

func (c *mcpClient) discoverTools(ctx context.Context) error {
	c.tools = nil
	c.appTools = make(map[string]mcpRemoteTool)
	var cursor string
	totalDeclarationBytes := 0
	totalTools := 0
	seenCursors := make(map[string]bool)
	for page := 0; page < maxMCPToolPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var listed struct {
			Tools      []mcpRemoteTool `json:"tools"`
			NextCursor string          `json:"nextCursor"`
		}
		if err := c.request(ctx, "tools/list", params, &listed); err != nil {
			return fmt.Errorf("list tools from %s: %w", c.cfg.Name, err)
		}
		for _, remote := range listed.Tools {
			totalTools++
			if totalTools > maxMCPTools {
				return fmt.Errorf("list tools from %s: server exposes more than %d tools", c.cfg.Name, maxMCPTools)
			}
			normalized, declarationBytes, err := normalizeMCPRemoteTool(remote)
			if err != nil {
				return fmt.Errorf("list tools from %s: %w", c.cfg.Name, err)
			}
			modelVisible, appVisible, err := mcpToolVisibility(normalized)
			if err != nil {
				return fmt.Errorf("list tools from %s: %w", c.cfg.Name, err)
			}
			totalDeclarationBytes += declarationBytes
			if totalDeclarationBytes > maxMCPDeclarationsBytes {
				return fmt.Errorf("list tools from %s: declarations exceed the 4 MB total limit", c.cfg.Name)
			}
			if appVisible {
				c.appTools[normalized.Name] = normalized
			}
			if modelVisible {
				c.tools = append(c.tools, normalized)
			}
		}
		if listed.NextCursor == "" {
			return nil
		}
		if seenCursors[listed.NextCursor] {
			return fmt.Errorf("list tools from %s: pagination cursor repeated", c.cfg.Name)
		}
		seenCursors[listed.NextCursor] = true
		cursor = listed.NextCursor
	}
	return fmt.Errorf("list tools from %s: pagination exceeds %d pages", c.cfg.Name, maxMCPToolPages)
}

func (c *mcpClient) notify(method string, params any) error {
	if c.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return c.http.notify(ctx, method, params)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return json.NewEncoder(c.stdin).Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *mcpClient) request(ctx context.Context, method string, params any, out any) error {
	if c.http != nil {
		return c.http.request(ctx, method, params, out)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	stopWatcher := make(chan struct{})
	watcherStopped := make(chan struct{})
	go func() {
		defer close(watcherStopped)
		select {
		case <-ctx.Done():
			// Prefer a completed request when completion and cancellation become
			// ready together. In particular, mcpTool.Execute cancels its timeout
			// context after request returns; without the join below a late watcher
			// could kill an otherwise healthy shared MCP process.
			select {
			case <-stopWatcher:
				return
			default:
				_ = c.Close()
			}
		case <-stopWatcher:
		}
	}()
	defer func() {
		close(stopWatcher)
		<-watcherStopped
	}()
	id := c.nextID.Add(1)
	if err := json.NewEncoder(c.stdin).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !c.scanner.Scan() {
			if err := c.scanner.Err(); err != nil {
				return err
			}
			detail := strings.TrimSpace(c.stderr.String())
			if detail != "" {
				return fmt.Errorf("server exited: %s", detail)
			}
			return io.ErrUnexpectedEOF
		}
		var resp mcpResponse
		if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
			continue // tolerate non-protocol diagnostic lines on stdout
		}
		var responseID int64
		if len(resp.ID) == 0 || json.Unmarshal(resp.ID, &responseID) != nil || responseID != id {
			continue // notification or unrelated message
		}
		if resp.Error != nil {
			return &mcpRemoteCallError{code: resp.Error.Code, message: resp.Error.Message}
		}
		if out != nil {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	}
}

func (c *mcpClient) Close() error {
	c.closeOnce.Do(func() {
		if c.onClose != nil {
			defer c.onClose()
		}
		if c.http != nil {
			_ = c.http.Close()
			return
		}
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
			// Wait through exec.Cmd, not os.Process: Cmd.Wait also joins the
			// stdout/stderr copy goroutines, closes their descriptors, and records
			// ProcessState. Process.Wait alone leaves that Cmd lifecycle unfinished.
			_ = c.cmd.Wait()
		}
	})
	return nil
}

type mcpTool struct {
	client           *mcpClient
	studio           *Studio
	projectID        string
	remoteName       string
	name             string
	desc             string
	schema           *genai.Schema
	appResourceURI   string
	onAppError       func(error)
	onTransportError func()
}

func (t *mcpTool) Name() string        { return t.name }
func (t *mcpTool) Description() string { return t.desc }
func (t *mcpTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: t.desc, Parameters: t.schema}
}
func (t *mcpTool) Validate(map[string]any) error { return nil }

type mcpToolCallResult struct {
	Content           []map[string]any `json:"content"`
	StructuredContent any              `json:"structuredContent,omitempty"`
	Meta              map[string]any   `json:"_meta,omitempty"`
	IsError           bool             `json:"isError,omitempty"`
}

func (t *mcpTool) Execute(ctx context.Context, args map[string]any) (tools.ToolResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, mcpToolCallTimeout)
	defer cancel()
	var result mcpToolCallResult
	err := t.client.request(callCtx, "tools/call", map[string]any{"name": t.remoteName, "arguments": args}, &result)
	if err != nil {
		var remoteErr *mcpRemoteCallError
		if !errors.As(err, &remoteErr) && t.onTransportError != nil {
			t.onTransportError()
		}
		return tools.NewErrorResult(err.Error()), err
	}
	var parts []string
	for _, item := range result.Content {
		itemType, _ := item["type"].(string)
		if itemType == "text" {
			if text, _ := item["text"].(string); text != "" {
				parts = append(parts, text)
			}
			continue
		}
		if data, err := json.MarshalIndent(item, "", "  "); err == nil {
			parts = append(parts, string(data))
		}
	}
	if result.StructuredContent != nil {
		if data, err := json.MarshalIndent(result.StructuredContent, "", "  "); err == nil {
			parts = append(parts, string(data))
		}
	}
	content := strings.Join(parts, "\n")
	if result.IsError {
		return tools.NewErrorResult(tools.TruncateToolResultContent(content, "")), nil
	}
	// structuredContent is already represented in content above. Returning it
	// again as ToolResult.Data would duplicate a potentially large object in
	// executors that serialize both fields.
	toolResult := tools.NewSuccessResult(tools.TruncateToolResultContent(content, ""))
	if t.appResourceURI != "" {
		app, err := t.client.readMCPApp(callCtx, t.appResourceURI, t.remoteName, args, result)
		if err != nil {
			if t.onAppError != nil {
				t.onAppError(err)
			}
		} else {
			if t.studio != nil && t.projectID != "" {
				projectID, sessionID := askUserRouting(ctx)
				if projectID != t.projectID {
					projectID = t.projectID
				}
				if sessionID == "" {
					sessionID = "default"
				}
				app.InstanceID = t.studio.ensureMCPAppRegistry().register(
					projectID, sessionID, app.ResourceURI, t.client,
				)
			}
			toolResult.Data = app
		}
	}
	return toolResult, nil
}

var mcpToolNameRE = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func mcpToolName(server, remote string) string {
	serverPart := strings.Trim(mcpToolNameRE.ReplaceAllString(server, "_"), "_")
	if serverPart == "" {
		serverPart = "server"
	}
	remotePart := strings.Trim(mcpToolNameRE.ReplaceAllString(remote, "_"), "_")
	if remotePart == "" {
		remotePart = "tool"
	}
	// Normalization alone is not injective ("foo-bar" and "foo bar" both
	// become "foo_bar"), and blindly truncating long names creates another
	// collision class. Keep a short stable suffix derived from the original
	// pair so every remote tool remains independently addressable.
	h := fnv.New32a()
	_, _ = h.Write([]byte(server))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(remote))
	suffix := fmt.Sprintf("_%08x", h.Sum32())
	name := "mcp_" + serverPart + "_" + remotePart
	if maxBase := 63 - len(suffix); len(name) > maxBase {
		name = strings.TrimRight(name[:maxBase], "_")
	}
	return name + suffix
}

func normalizeMCPRemoteTool(remote mcpRemoteTool) (mcpRemoteTool, int, error) {
	data, err := json.Marshal(remote)
	if err != nil {
		return remote, 0, fmt.Errorf("invalid tool declaration: %w", err)
	}
	if len(data) > maxMCPToolDeclarationBytes {
		return remote, 0, fmt.Errorf("tool declaration exceeds the 64 KB limit")
	}
	remote.Name = strings.TrimSpace(remote.Name)
	if remote.Name == "" || len([]rune(remote.Name)) > maxMCPRemoteToolNameRunes || strings.ContainsRune(remote.Name, 0) {
		return remote, 0, fmt.Errorf("invalid remote tool name")
	}
	remote.Description = truncateMCPRunes(strings.TrimSpace(remote.Description), maxMCPDescriptionRunes)
	nodes := 0
	if err := validateMCPSchema(remote.InputSchema, 0, &nodes); err != nil {
		return remote, 0, fmt.Errorf("tool %s has invalid input schema: %w", remote.Name, err)
	}
	return remote, len(data), nil
}

func truncateMCPRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func validateMCPSchema(raw map[string]any, depth int, nodes *int) error {
	if raw == nil {
		return nil
	}
	if depth > maxMCPSchemaDepth {
		return fmt.Errorf("nesting exceeds %d levels", maxMCPSchemaDepth)
	}
	*nodes = *nodes + 1
	if *nodes > maxMCPSchemaNodes {
		return fmt.Errorf("schema exceeds %d nodes", maxMCPSchemaNodes)
	}
	if props, ok := raw["properties"].(map[string]any); ok {
		for _, value := range props {
			if child, ok := value.(map[string]any); ok {
				if err := validateMCPSchema(child, depth+1, nodes); err != nil {
					return err
				}
			}
		}
	}
	if items, ok := raw["items"].(map[string]any); ok {
		if err := validateMCPSchema(items, depth+1, nodes); err != nil {
			return err
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if variants, ok := raw[keyword].([]any); ok {
			for _, value := range variants {
				if child, ok := value.(map[string]any); ok {
					if err := validateMCPSchema(child, depth+1, nodes); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func schemaFromMCP(raw map[string]any) *genai.Schema {
	return schemaFromMCPDepth(raw, 0)
}

func schemaFromMCPDepth(raw map[string]any, depth int) *genai.Schema {
	if raw == nil {
		return &genai.Schema{Type: genai.TypeObject}
	}
	if depth > maxMCPSchemaDepth {
		return &genai.Schema{Type: genai.TypeObject, Description: "Schema truncated at nesting limit"}
	}
	s := &genai.Schema{}
	switch raw["type"] {
	case "string":
		s.Type = genai.TypeString
	case "number":
		s.Type = genai.TypeNumber
	case "integer":
		s.Type = genai.TypeInteger
	case "boolean":
		s.Type = genai.TypeBoolean
	case "array":
		s.Type = genai.TypeArray
	case "object":
		s.Type = genai.TypeObject
	default:
		s.Type = genai.TypeObject
	}
	s.Description, _ = raw["description"].(string)
	s.Description = truncateMCPRunes(s.Description, maxMCPDescriptionRunes)
	if required, ok := raw["required"].([]any); ok {
		for _, item := range required {
			if value, ok := item.(string); ok {
				s.Required = append(s.Required, value)
			}
		}
	}
	if enum, ok := raw["enum"].([]any); ok {
		for _, item := range enum {
			s.Enum = append(s.Enum, fmt.Sprint(item))
		}
	}
	if props, ok := raw["properties"].(map[string]any); ok {
		s.Properties = make(map[string]*genai.Schema, len(props))
		for name, value := range props {
			if child, ok := value.(map[string]any); ok {
				s.Properties[name] = schemaFromMCPDepth(child, depth+1)
			}
		}
	}
	if items, ok := raw["items"].(map[string]any); ok {
		s.Items = schemaFromMCPDepth(items, depth+1)
	}
	return s
}

func (p *Project) registerMCPTools(ctx context.Context, reg *tools.Registry) {
	if p.studio == nil {
		return
	}
	configs, err := loadMCPServers()
	if err != nil {
		p.studio.LogEvent("error", "mcp", err.Error())
		return
	}
	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		mc, err := connectMCP(connectCtx, cfg)
		cancel()
		if err != nil {
			p.studio.LogEvent("error", "mcp", fmt.Sprintf("%s: %v", cfg.Name, err))
			continue
		}
		mc.onClose = func() {
			p.studio.ensureMCPAppRegistry().unregisterClient(mc)
		}
		p.mcpClients = append(p.mcpClients, mc)
		for _, remote := range mc.tools {
			name := mcpToolName(cfg.Name, remote.Name)
			desc := strings.TrimSpace(remote.Description)
			if desc == "" {
				desc = "MCP tool " + remote.Name + " from " + cfg.Name
			}
			appResourceURI, appErr := mcpAppResourceURI(remote)
			if appErr != nil {
				p.studio.LogEvent("warn", "mcp-app", fmt.Sprintf("%s/%s: %v", cfg.Name, remote.Name, appErr))
				appResourceURI = ""
			}
			if err := reg.Register(&mcpTool{
				client: mc, studio: p.studio, projectID: p.ID,
				remoteName: remote.Name, name: name,
				desc: desc, schema: schemaFromMCP(remote.InputSchema),
				appResourceURI: appResourceURI,
				onAppError: func(err error) {
					p.studio.LogEvent("warn", "mcp-app", fmt.Sprintf("%s/%s: %v", cfg.Name, remote.Name, err))
				},
				onTransportError: func() { p.mcpTransportBroken.Store(true) },
			}); err != nil {
				p.studio.LogEvent("warn", "mcp", fmt.Sprintf("%s/%s: %v", cfg.Name, remote.Name, err))
			}
		}
	}
}

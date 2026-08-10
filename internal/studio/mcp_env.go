package studio

import (
	"fmt"
	"os"
	"strings"
)

// resolveMCPConfigEnvironment expands only ${NAME} and ${NAME:-fallback}
// placeholders. It does not invoke a shell, and the resolved values exist only
// in the short-lived connector configuration used to establish a connection.
// This lets plugin definitions retain secret references instead of copying
// secret values into mcp_servers.json.
func resolveMCPConfigEnvironment(cfg MCPServerConfig) (MCPServerConfig, error) {
	cfg.Args = append([]string(nil), cfg.Args...)
	cfg.Env = cloneStringMap(cfg.Env)
	cfg.Headers = cloneStringMap(cfg.Headers)
	resolve := func(field, value string) (string, error) {
		expanded, err := expandMCPEnvironment(value, os.LookupEnv)
		if err != nil {
			return "", fmt.Errorf("%s for MCP connector %q: %w", field, cfg.Name, err)
		}
		return expanded, nil
	}

	var err error
	if cfg.Command, err = resolve("command", cfg.Command); err != nil {
		return cfg, err
	}
	for i := range cfg.Args {
		if cfg.Args[i], err = resolve(fmt.Sprintf("argument %d", i+1), cfg.Args[i]); err != nil {
			return cfg, err
		}
	}
	for key, value := range cfg.Env {
		if cfg.Env[key], err = resolve("environment variable "+key, value); err != nil {
			return cfg, err
		}
	}
	if cfg.URL, err = resolve("URL", cfg.URL); err != nil {
		return cfg, err
	}
	for key, value := range cfg.Headers {
		if cfg.Headers[key], err = resolve("HTTP header "+key, value); err != nil {
			return cfg, err
		}
	}
	if cfg.OAuthClientID, err = resolve("OAuth client ID", cfg.OAuthClientID); err != nil {
		return cfg, err
	}
	return validateMCPConfig(cfg)
}

func expandMCPEnvironment(value string, lookup func(string) (string, bool)) (string, error) {
	if !strings.Contains(value, "${") {
		return value, nil
	}
	var out strings.Builder
	out.Grow(len(value))
	for cursor := 0; cursor < len(value); {
		start := strings.Index(value[cursor:], "${")
		if start < 0 {
			out.WriteString(value[cursor:])
			break
		}
		start += cursor
		out.WriteString(value[cursor:start])
		endOffset := strings.IndexByte(value[start+2:], '}')
		if endOffset < 0 {
			return "", fmt.Errorf("unterminated environment placeholder")
		}
		end := start + 2 + endOffset
		expression := value[start+2 : end]
		name := expression
		fallback := ""
		hasFallback := false
		if separator := strings.Index(expression, ":-"); separator >= 0 {
			name = expression[:separator]
			fallback = expression[separator+2:]
			hasFallback = true
		}
		if !mcpEnvKeyRE.MatchString(name) {
			return "", fmt.Errorf("invalid environment placeholder %q", expression)
		}
		resolved, found := lookup(name)
		if !found || (resolved == "" && hasFallback) {
			if !hasFallback {
				return "", fmt.Errorf("required environment variable %s is not set", name)
			}
			resolved = fallback
		}
		out.WriteString(resolved)
		cursor = end + 1
	}
	return out.String(), nil
}

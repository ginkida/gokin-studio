package security

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/logging"
)

// SandboxConfig holds sandbox configuration
type SandboxConfig struct {
	// Enabled determines if sandboxing is active
	Enabled bool
	// RootDir is the root directory for chroot (empty = use current workDir)
	RootDir string
	// EnableSeccomp enables seccomp-bpf syscall filtering (Linux only)
	EnableSeccomp bool
	// ReadOnly makes the sandbox filesystem read-only
	ReadOnly bool
	// AllowNetwork grants the isolated process access to the host network
	// namespace. It is false by default: callers must surface a fresh,
	// exact-action approval before enabling it for model-requested code.
	AllowNetwork bool
}

// DefaultSandboxConfig returns the default sandbox configuration
func DefaultSandboxConfig() SandboxConfig {
	return SandboxConfig{
		Enabled:       true,
		EnableSeccomp: false, // Disabled by default (requires libseccomp)
		ReadOnly:      false,
		AllowNetwork:  false,
	}
}

// SandboxResult represents the result of a sandboxed command execution
type SandboxResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Error    error
}

const sandboxOutputMaxBytes = 2 << 20

// WorkspaceIsolationStatus describes the platform sandbox used for commands
// that execute project-controlled code. Available=false means the caller must
// either fail closed or obtain a fresh, explicit host-execution approval.
type WorkspaceIsolationStatus struct {
	Available bool   `json:"available"`
	Enforced  bool   `json:"enforced"`
	Mode      string `json:"mode"`
	Detail    string `json:"detail"`
}

// SandboxedCommand represents a command that will be executed in a sandbox
type SandboxedCommand struct {
	ctx        context.Context
	cmd        *exec.Cmd
	config     SandboxConfig
	workDir    string
	runtimeDir string
	mode       string
}

// NewSandboxedCommand creates a new sandboxed command
func NewSandboxedCommand(ctx context.Context, workDir string, command string, config SandboxConfig) (*SandboxedCommand, error) {
	return newSandboxedCommand(ctx, workDir, "bash", []string{"--noprofile", "--norc", "-c", command}, config)
}

// NewSandboxedCommandArgs creates a workspace-isolated direct-exec command.
// program and args are passed without shell interpretation.
func NewSandboxedCommandArgs(
	ctx context.Context,
	workDir string,
	program string,
	args []string,
	config SandboxConfig,
) (*SandboxedCommand, error) {
	if strings.TrimSpace(program) == "" {
		return nil, fmt.Errorf("program cannot be empty")
	}
	return newSandboxedCommand(ctx, workDir, program, args, config)
}

func newSandboxedCommand(
	ctx context.Context,
	workDir string,
	program string,
	args []string,
	config SandboxConfig,
) (*SandboxedCommand, error) {
	// Validate workDir before doing anything
	if workDir == "" {
		return nil, fmt.Errorf("workDir cannot be empty")
	}

	// Resolve absolute path to prevent directory traversal
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workDir: %w", err)
	}

	// Resolve symlinks so a project opened through an alias cannot weaken a
	// path-based sandbox profile.
	realWorkDir, err := filepath.EvalSymlinks(absWorkDir)
	if err != nil {
		return nil, fmt.Errorf("workDir does not exist: %s", absWorkDir)
	}
	info, err := os.Stat(realWorkDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("workDir is not a directory: %s", absWorkDir)
	}

	runtimeDir, err := workspaceSandboxRuntimeDir(realWorkDir)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = realWorkDir

	// Never expose the real user HOME, XDG directories, language caches, or
	// credential-bearing environment to project-controlled processes.
	cmd.Env = safeEnvironment(realWorkDir, runtimeDir)

	sandboxed := &SandboxedCommand{
		ctx:        ctx,
		cmd:        cmd,
		config:     config,
		workDir:    realWorkDir,
		runtimeDir: runtimeDir,
	}

	// Apply sandboxing if enabled
	if config.Enabled {
		if err := sandboxed.applySandbox(absWorkDir); err != nil {
			return nil, fmt.Errorf("failed to apply sandbox: %w", err)
		}
	}

	return sandboxed, nil
}

func workspaceSandboxRuntimeDir(workDir string) (string, error) {
	sum := sha256.Sum256([]byte(workDir))
	key := hex.EncodeToString(sum[:12])
	root := filepath.Join(os.TempDir(), "gokin-studio-shell", key)
	for _, dir := range []string{
		root,
		filepath.Join(root, "home"),
		filepath.Join(root, "tmp"),
		filepath.Join(root, "cache"),
		filepath.Join(root, "config"),
		filepath.Join(root, "data"),
		filepath.Join(root, "go"),
		filepath.Join(root, "go-build"),
		filepath.Join(root, "npm"),
		filepath.Join(root, "pip"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create isolated shell runtime: %w", err)
		}
	}
	return root, nil
}

// sandboxPATH returns a safe PATH that includes platform-specific directories.
func sandboxPATH(workDir string) string {
	paths := []string{
		filepath.Join(workDir, "node_modules", ".bin"),
		filepath.Join(workDir, ".venv", "bin"),
		filepath.Join(workDir, "venv", "bin"),
		filepath.Join(runtime.GOROOT(), "bin"),
	}
	if runtime.GOOS == "darwin" {
		// Homebrew on Apple Silicon installs to /opt/homebrew/bin
		paths = append(paths, "/opt/homebrew/bin", "/opt/homebrew/sbin")
	}
	paths = append(paths, "/usr/local/bin", "/usr/local/sbin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	return strings.Join(paths, string(os.PathListSeparator))
}

// safeEnvironment returns a sanitized environment with safe defaults
func safeEnvironment(workDir, runtimeDir string) []string {
	safeVars := map[string]string{
		"PATH":             sandboxPATH(workDir),
		"HOME":             filepath.Join(runtimeDir, "home"),
		"USER":             os.Getenv("USER"),
		"TERM":             "xterm",
		"LANG":             "en_US.UTF-8",
		"LC_ALL":           "en_US.UTF-8",
		"PWD":              workDir,
		"TMPDIR":           filepath.Join(runtimeDir, "tmp"),
		"TMP":              filepath.Join(runtimeDir, "tmp"),
		"TEMP":             filepath.Join(runtimeDir, "tmp"),
		"SHELL":            "/bin/bash",
		"XDG_CONFIG_HOME":  filepath.Join(runtimeDir, "config"),
		"XDG_DATA_HOME":    filepath.Join(runtimeDir, "data"),
		"XDG_CACHE_HOME":   filepath.Join(runtimeDir, "cache"),
		"GOPATH":           filepath.Join(runtimeDir, "go"),
		"GOCACHE":          filepath.Join(runtimeDir, "go-build"),
		"GOROOT":           runtime.GOROOT(),
		"GOPROXY":          os.Getenv("GOPROXY"),
		"NPM_CONFIG_CACHE": filepath.Join(runtimeDir, "npm"),
		"PIP_CACHE_DIR":    filepath.Join(runtimeDir, "pip"),
		"EDITOR":           os.Getenv("EDITOR"),
		"VISUAL":           os.Getenv("VISUAL"),
	}

	for name, value := range WorkspaceEnvironmentSnapshot() {
		safeVars[name] = value
	}

	// Build environment array
	env := make([]string, 0, len(safeVars))
	for k, v := range safeVars {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// WorkspaceSafeEnvironment returns the same credential-minimising environment
// used inside the filesystem sandbox. It is also used for explicitly approved
// host execution so HOME/XDG/cache paths never point at the user's real data.
func WorkspaceSafeEnvironment(workDir string) ([]string, error) {
	absolute, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace environment: %w", err)
	}
	runtimeDir, err := workspaceSandboxRuntimeDir(absolute)
	if err != nil {
		return nil, err
	}
	return safeEnvironment(absolute, runtimeDir), nil
}

// Command returns the prepared isolated command. Callers that need streaming
// can attach stdin/stdout/stderr before starting it.
func (sc *SandboxedCommand) Command() *exec.Cmd {
	return sc.cmd
}

// IsolationMode identifies the platform isolation actually applied.
func (sc *SandboxedCommand) IsolationMode() string {
	return sc.mode
}

// Run runs the sandboxed command and returns the result
func (sc *SandboxedCommand) Run(timeout time.Duration) *SandboxResult {
	result := &SandboxResult{}

	// Capture stdout and stderr
	stdout, err := sc.cmd.StdoutPipe()
	if err != nil {
		result.Error = fmt.Errorf("failed to create stdout pipe: %w", err)
		return result
	}

	stderr, err := sc.cmd.StderrPipe()
	if err != nil {
		result.Error = fmt.Errorf("failed to create stderr pipe: %w", err)
		return result
	}

	// Start the command
	if err := sc.cmd.Start(); err != nil {
		result.Error = fmt.Errorf("failed to start command: %w", err)
		return result
	}

	// Start the fallback timer only after cmd.Start has finished publishing the
	// Process pointer. Starting it earlier races with os/exec on short timeouts.
	if timeout > 0 {
		process := sc.cmd.Process
		timer := time.AfterFunc(timeout, func() {
			_ = process.Kill()
		})
		defer timer.Stop()
	}

	// Read stdout and stderr concurrently to avoid goroutine leaks and
	// cmd.Wait() hangs when one pipe times out while the other is still open.
	type pipeResult struct {
		data []byte
		err  error
	}
	stdoutCh := make(chan pipeResult, 1)
	stderrCh := make(chan pipeResult, 1)

	go func() {
		data, err := readWithTimeout(stdout, timeout)
		stdoutCh <- pipeResult{data, err}
	}()
	go func() {
		data, err := readWithTimeout(stderr, timeout)
		stderrCh <- pipeResult{data, err}
	}()

	stdoutRes := <-stdoutCh
	stderrRes := <-stderrCh
	result.Stdout = stdoutRes.data
	result.Stderr = stderrRes.data
	if stdoutRes.err != nil {
		logging.Debug("failed to read sandbox stdout", "error", stdoutRes.err)
	}
	if stderrRes.err != nil {
		logging.Debug("failed to read sandbox stderr", "error", stderrRes.err)
	}

	// Wait for command to finish (safe now — both pipes are drained or timed out)
	err = sc.cmd.Wait()

	// Get exit code
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			result.Error = nil // Exit code is in result.ExitCode
		} else {
			result.Error = err
		}
	}

	return result
}

// readWithTimeout reads from a pipe with a timeout.
// It reads all available data from the pipe until EOF or timeout.
func readWithTimeout(pipe interface{}, timeout time.Duration) ([]byte, error) {
	reader, ok := pipe.(io.Reader)
	if !ok {
		return nil, fmt.Errorf("pipe is not an io.Reader")
	}

	// Create a channel for the read result
	type readResult struct {
		data []byte
		err  error
	}
	resultChan := make(chan readResult, 1)

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Read in a goroutine
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Error("panic in sandbox reader", "error", r)
				resultChan <- readResult{err: fmt.Errorf("panic in sandbox reader: %v", r)}
			}
		}()
		var buf bytes.Buffer
		limited := io.LimitReader(reader, sandboxOutputMaxBytes+1)
		_, err := io.Copy(&buf, limited)
		data := buf.Bytes()
		if len(data) > sandboxOutputMaxBytes {
			data = append(append([]byte(nil), data[:sandboxOutputMaxBytes]...),
				[]byte("\n... [sandbox output truncated at 2 MiB] ...\n")...)
			// Continue draining so a verbose child cannot block in write(2)
			// while the parent waits for process exit.
			if _, drainErr := io.Copy(io.Discard, reader); err == nil {
				err = drainErr
			}
		}
		resultChan <- readResult{data: data, err: err}
	}()

	// Wait for either completion or timeout
	select {
	case <-ctx.Done():
		// Timeout occurred - return what we have (empty)
		// Note: We can't cancel the io.Copy, but the process will be killed
		// by the parent, which will close the pipe and unblock the goroutine
		return nil, fmt.Errorf("read timeout after %v", timeout)
	case result := <-resultChan:
		return result.data, result.err
	}
}

// IsSandboxSupported checks if the current system supports sandboxing features
func IsSandboxSupported() (chroot, seccomp bool) {
	status := DetectWorkspaceIsolation()
	return status.Available, status.Available && runtime.GOOS == "linux"
}

// IsLinux checks if the current OS is Linux
func IsLinux() bool {
	return runtime.GOOS == "linux"
}

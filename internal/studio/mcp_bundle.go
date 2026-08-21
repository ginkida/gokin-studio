package studio

import (
	"archive/zip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	maxMCPBundleBytes             = 128 << 20
	maxMCPBundleExpandedBytes     = 256 << 20
	maxMCPBundleFiles             = 10_000
	maxMCPBundleManifestBytes     = 256 << 10
	maxMCPBundleConfigFields      = 64
	maxMCPBundleConfigValueBytes  = 16 << 10
	maxMCPBundleMultipleValues    = 32
	maxMCPBundlePathBytes         = 768
	maxMCPBundleDescriptionRunes  = 4_000
	maxMCPBundleDeclaredToolNames = 64
)

var (
	mcpBundleNameRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	mcpBundleVersionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	mcpBundleVarRE     = regexp.MustCompile(`\$\{[^{}]+\}`)
	mcpUserConfigVarRE = regexp.MustCompile(`^\$\{user_config\.([A-Za-z_][A-Za-z0-9_]*)\}$`)
)

// MCPBundleConfigField is a manifest-declared value collected before an MCPB
// is installed. Default remains JSON-shaped so boolean/number/array defaults
// arrive in the frontend without lossy string conversion.
type MCPBundleConfigField struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required"`
	Multiple    bool     `json:"multiple,omitempty"`
	Sensitive   bool     `json:"sensitive,omitempty"`
	Default     any      `json:"default,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
}

// MCPBundlePreview contains only reviewable manifest metadata plus a digest.
// InstallMCPBundle re-hashes and re-parses the selected file, preventing a
// package swapped between the review and confirmation steps from being run.
type MCPBundlePreview struct {
	Path           string                 `json:"path"`
	Digest         string                 `json:"digest"`
	Name           string                 `json:"name"`
	DisplayName    string                 `json:"displayName"`
	Version        string                 `json:"version"`
	Description    string                 `json:"description"`
	Author         string                 `json:"author"`
	ServerType     string                 `json:"serverType"`
	Tools          []string               `json:"tools"`
	ConfigFields   []MCPBundleConfigField `json:"configFields"`
	Warnings       []string               `json:"warnings"`
	ExistingServer bool                   `json:"existingServer"`
}

type mcpBundleManifest struct {
	ManifestVersion string                         `json:"manifest_version"`
	Name            string                         `json:"name"`
	DisplayName     string                         `json:"display_name"`
	Version         string                         `json:"version"`
	Description     string                         `json:"description"`
	LongDescription string                         `json:"long_description"`
	Author          mcpBundleAuthor                `json:"author"`
	Server          mcpBundleServer                `json:"server"`
	Compatibility   mcpBundleCompatibility         `json:"compatibility"`
	UserConfig      map[string]mcpBundleConfigSpec `json:"user_config"`
	Tools           []mcpBundleTool                `json:"tools"`
}

type mcpBundleAuthor struct {
	Name string `json:"name"`
}

type mcpBundleServer struct {
	Type       string             `json:"type"`
	EntryPoint string             `json:"entry_point"`
	MCPConfig  mcpBundleMCPConfig `json:"mcp_config"`
}

type mcpBundleMCPConfig struct {
	Command           string                            `json:"command"`
	Args              []string                          `json:"args"`
	Env               map[string]string                 `json:"env"`
	PlatformOverrides map[string]mcpBundlePlatformPatch `json:"platform_overrides"`
}

type mcpBundlePlatformPatch struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

type mcpBundleCompatibility struct {
	Platforms []string `json:"platforms"`
}

type mcpBundleConfigSpec struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Multiple    bool     `json:"multiple"`
	Sensitive   bool     `json:"sensitive"`
	Default     any      `json:"default"`
	Min         *float64 `json:"min"`
	Max         *float64 `json:"max"`
}

type mcpBundleTool struct {
	Name string `json:"name"`
}

type parsedMCPBundle struct {
	manifest mcpBundleManifest
	digest   string
	files    map[string]*zip.File
	entries  []*zip.File
	archive  *os.File
}

type mcpBundleValue struct {
	scalar string
	array  []string
}

// SelectMCPBundle opens the OS picker and returns a review-only manifest. A
// nil result means the user cancelled the native dialog.
func (s *Studio) SelectMCPBundle() (*MCPBundlePreview, error) {
	selected, err := wailsRuntime.OpenFileDialog(s.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Install MCP Bundle",
		Filters: []wailsRuntime.FileFilter{{
			DisplayName: "MCP Bundles",
			Pattern:     "*.mcpb",
		}},
	})
	if err != nil {
		return nil, err
	}
	if selected == "" {
		return nil, nil
	}
	return s.previewMCPBundle(selected)
}

// BrowseMCPBundleConfigPath backs manifest file/directory fields with native
// pickers. Multiple fields call this repeatedly and append each approved path.
func (s *Studio) BrowseMCPBundleConfigPath(kind string) (string, error) {
	switch kind {
	case "directory":
		return wailsRuntime.OpenDirectoryDialog(s.ctx, wailsRuntime.OpenDialogOptions{
			Title: "Select Directory for MCP Extension",
		})
	case "file":
		return wailsRuntime.OpenFileDialog(s.ctx, wailsRuntime.OpenDialogOptions{
			Title: "Select File for MCP Extension",
		})
	default:
		return "", fmt.Errorf("MCPB configuration path type must be file or directory")
	}
}

func (s *Studio) previewMCPBundle(bundlePath string) (*MCPBundlePreview, error) {
	bundle, err := openMCPBundle(bundlePath)
	if err != nil {
		return nil, err
	}
	defer bundle.archive.Close()

	manifest := bundle.manifest
	fields, err := bundlePreviewFields(manifest.UserConfig)
	if err != nil {
		return nil, err
	}
	preview := &MCPBundlePreview{
		Path:         bundlePath,
		Digest:       bundle.digest,
		Name:         manifest.Name,
		DisplayName:  manifest.DisplayName,
		Version:      manifest.Version,
		Description:  truncateUTF8(strings.TrimSpace(manifest.Description), maxMCPBundleDescriptionRunes),
		Author:       strings.TrimSpace(manifest.Author.Name),
		ServerType:   manifest.Server.Type,
		ConfigFields: fields,
		Warnings: []string{
			"MCP Bundles run local code with your user permissions. Install only packages you trust.",
			"Publisher signatures are not verified by this Gokin Studio build.",
		},
	}
	if preview.DisplayName == "" {
		preview.DisplayName = preview.Name
	}
	if manifest.Server.Type == "node" || manifest.Server.Type == "python" {
		preview.Warnings = append(preview.Warnings,
			fmt.Sprintf("This bundle requires a compatible %s runtime available on this computer.", manifest.Server.Type))
	}
	for _, tool := range manifest.Tools {
		if len(preview.Tools) >= maxMCPBundleDeclaredToolNames {
			break
		}
		if name := strings.TrimSpace(tool.Name); name != "" {
			preview.Tools = append(preview.Tools, name)
		}
	}
	configs, err := loadMCPServersRaw()
	if err != nil {
		return nil, err
	}
	for _, cfg := range configs {
		if strings.EqualFold(strings.TrimSpace(cfg.Name), manifest.Name) {
			preview.ExistingServer = true
			preview.Warnings = append(preview.Warnings, "Installing will replace the existing connector with the same name.")
			break
		}
	}
	return preview, nil
}

// InstallMCPBundle verifies the reviewed digest, safely extracts the archive
// into the app-owned bundle directory, resolves manifest variables, then saves
// a normal stdio connector. enable=false is the conservative default in UI:
// users can Test the package before exposing its tools to GLM/Kimi.
func (s *Studio) InstallMCPBundle(bundlePath, reviewedDigest string, userValues map[string]any, enable bool) (*MCPServerStatus, error) {
	bundle, err := openMCPBundle(bundlePath)
	if err != nil {
		return nil, err
	}
	defer bundle.archive.Close()
	if len(reviewedDigest) != sha256.Size*2 {
		return nil, fmt.Errorf("invalid reviewed bundle digest")
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(reviewedDigest)), []byte(bundle.digest)) != 1 {
		return nil, fmt.Errorf("MCP bundle changed after review; select it again")
	}
	values, err := validateMCPBundleValues(bundle.manifest.UserConfig, userValues)
	if err != nil {
		return nil, err
	}
	installDir, err := extractMCPBundle(bundle)
	if err != nil {
		return nil, err
	}
	cfg, err := bundleServerConfig(bundle.manifest, installDir, values, enable)
	if err != nil {
		return nil, err
	}
	if err := s.SaveMCPServer(cfg); err != nil {
		return nil, err
	}
	return &MCPServerStatus{MCPServerConfig: cfg}, nil
}

func openMCPBundle(bundlePath string) (*parsedMCPBundle, error) {
	if !utf8.ValidString(bundlePath) || strings.ContainsRune(bundlePath, 0) {
		return nil, fmt.Errorf("invalid MCP bundle path")
	}
	if !strings.EqualFold(filepath.Ext(bundlePath), ".mcpb") {
		return nil, fmt.Errorf("select a .mcpb bundle")
	}
	info, err := os.Lstat(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("stat MCP bundle: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("MCP bundle must be a regular non-symlink file")
	}
	if info.Size() <= 0 || info.Size() > maxMCPBundleBytes {
		return nil, fmt.Errorf("MCP bundle must be between 1 byte and %d MiB", maxMCPBundleBytes>>20)
	}
	archive, err := os.Open(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("open MCP bundle archive: %w", err)
	}
	archiveInfo, err := archive.Stat()
	if err != nil {
		_ = archive.Close()
		return nil, fmt.Errorf("stat open MCP bundle: %w", err)
	}
	if !archiveInfo.Mode().IsRegular() || !sameOpenedFile(info, archiveInfo) ||
		archiveInfo.Size() <= 0 || archiveInfo.Size() > maxMCPBundleBytes {
		_ = archive.Close()
		return nil, fmt.Errorf("MCP bundle changed while it was being opened")
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(archive, maxMCPBundleBytes+1))
	if err != nil {
		_ = archive.Close()
		return nil, fmt.Errorf("hash MCP bundle: %w", err)
	}
	if count > maxMCPBundleBytes {
		_ = archive.Close()
		return nil, fmt.Errorf("MCP bundle exceeds the %d MiB limit", maxMCPBundleBytes>>20)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		_ = archive.Close()
		return nil, fmt.Errorf("rewind MCP bundle: %w", err)
	}
	zipReader, err := zip.NewReader(archive, archiveInfo.Size())
	if err != nil {
		_ = archive.Close()
		return nil, fmt.Errorf("open MCP bundle archive: %w", err)
	}
	closeWithError := func(err error) (*parsedMCPBundle, error) {
		_ = archive.Close()
		return nil, err
	}
	if len(zipReader.File) == 0 || len(zipReader.File) > maxMCPBundleFiles {
		return closeWithError(fmt.Errorf("MCP bundle must contain 1 to %d entries", maxMCPBundleFiles))
	}

	files := make(map[string]*zip.File, len(zipReader.File))
	var expanded uint64
	var manifestFile *zip.File
	for _, file := range zipReader.File {
		clean, err := cleanMCPBundleEntry(file.Name)
		if err != nil {
			return closeWithError(err)
		}
		key := strings.ToLower(clean)
		if _, duplicate := files[key]; duplicate {
			return closeWithError(fmt.Errorf("duplicate MCP bundle entry: %s", clean))
		}
		files[key] = file
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return closeWithError(fmt.Errorf("unsupported MCP bundle entry type: %s", clean))
		}
		if file.UncompressedSize64 > maxMCPBundleExpandedBytes ||
			expanded > maxMCPBundleExpandedBytes-file.UncompressedSize64 {
			return closeWithError(fmt.Errorf("MCP bundle exceeds the %d MiB expanded-size limit", maxMCPBundleExpandedBytes>>20))
		}
		expanded += file.UncompressedSize64
		if clean == "manifest.json" {
			if mode.IsDir() {
				return closeWithError(fmt.Errorf("manifest.json must be a file"))
			}
			manifestFile = file
		}
	}
	if manifestFile == nil {
		return closeWithError(fmt.Errorf("MCP bundle has no root manifest.json"))
	}
	manifestData, err := readZipFileLimited(manifestFile, maxMCPBundleManifestBytes)
	if err != nil {
		return closeWithError(fmt.Errorf("read MCP bundle manifest: %w", err))
	}
	var manifest mcpBundleManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return closeWithError(fmt.Errorf("parse MCP bundle manifest: %w", err))
	}
	if err := validateMCPBundleManifest(&manifest, files); err != nil {
		return closeWithError(err)
	}
	return &parsedMCPBundle{
		manifest: manifest,
		digest:   digest,
		files:    files,
		entries:  zipReader.File,
		archive:  archive,
	}, nil
}

func validateMCPBundleManifest(manifest *mcpBundleManifest, files map[string]*zip.File) error {
	manifest.ManifestVersion = strings.TrimSpace(manifest.ManifestVersion)
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.DisplayName = strings.TrimSpace(manifest.DisplayName)
	manifest.Version = strings.TrimSpace(manifest.Version)
	manifest.Description = strings.TrimSpace(manifest.Description)
	manifest.Author.Name = strings.TrimSpace(manifest.Author.Name)
	manifest.Server.EntryPoint = strings.TrimSpace(manifest.Server.EntryPoint)
	if manifest.ManifestVersion != "0.3" && manifest.ManifestVersion != "0.4" {
		return fmt.Errorf("unsupported MCPB manifest version %q (supported: 0.3, 0.4)", manifest.ManifestVersion)
	}
	if !mcpBundleNameRE.MatchString(manifest.Name) || len(manifest.Name) > 50 {
		return fmt.Errorf("invalid MCPB name")
	}
	if !mcpBundleVersionRE.MatchString(manifest.Version) {
		return fmt.Errorf("invalid MCPB semantic version")
	}
	if strings.TrimSpace(manifest.Description) == "" ||
		len([]rune(manifest.Description)) > maxMCPBundleDescriptionRunes {
		return fmt.Errorf("MCPB description is required and must be at most %d characters", maxMCPBundleDescriptionRunes)
	}
	if strings.TrimSpace(manifest.Author.Name) == "" || len([]rune(manifest.Author.Name)) > 200 {
		return fmt.Errorf("MCPB author name is required")
	}
	manifest.Server.Type = strings.ToLower(strings.TrimSpace(manifest.Server.Type))
	switch manifest.Server.Type {
	case "node", "python", "binary":
	case "uv":
		return fmt.Errorf("UV-based MCP Bundles are not supported because this build does not bundle the UV runtime")
	default:
		return fmt.Errorf("unsupported MCPB server type %q", manifest.Server.Type)
	}
	entry, err := cleanMCPBundleEntry(manifest.Server.EntryPoint)
	if err != nil || entry == "." || strings.HasSuffix(entry, "/") {
		return fmt.Errorf("invalid MCPB server entry_point")
	}
	entryFile := files[strings.ToLower(entry)]
	if entryFile == nil || !entryFile.Mode().IsRegular() {
		return fmt.Errorf("MCPB server entry_point is missing: %s", entry)
	}
	if len(manifest.UserConfig) > maxMCPBundleConfigFields {
		return fmt.Errorf("MCPB declares more than %d configuration fields", maxMCPBundleConfigFields)
	}
	for key, spec := range manifest.UserConfig {
		if !mcpEnvKeyRE.MatchString(key) {
			return fmt.Errorf("invalid MCPB user_config key %q", key)
		}
		switch spec.Type {
		case "string", "number", "boolean", "directory", "file":
		default:
			return fmt.Errorf("unsupported MCPB user_config type %q for %s", spec.Type, key)
		}
		if spec.Multiple && spec.Type != "directory" && spec.Type != "file" {
			return fmt.Errorf("MCPB user_config %s uses multiple with a non-path type", key)
		}
		if spec.Min != nil && (math.IsNaN(*spec.Min) || math.IsInf(*spec.Min, 0)) {
			return fmt.Errorf("invalid minimum for MCPB user_config %s", key)
		}
		if spec.Max != nil && (math.IsNaN(*spec.Max) || math.IsInf(*spec.Max, 0)) {
			return fmt.Errorf("invalid maximum for MCPB user_config %s", key)
		}
		if spec.Min != nil && spec.Max != nil && *spec.Min > *spec.Max {
			return fmt.Errorf("minimum exceeds maximum for MCPB user_config %s", key)
		}
	}
	platform := mcpBundlePlatform()
	if len(manifest.Compatibility.Platforms) > 0 {
		supported := false
		for _, candidate := range manifest.Compatibility.Platforms {
			if candidate == platform {
				supported = true
				break
			}
		}
		if !supported {
			return fmt.Errorf("MCPB does not support this platform (%s)", platform)
		}
	}
	return nil
}

func bundlePreviewFields(specs map[string]mcpBundleConfigSpec) ([]MCPBundleConfigField, error) {
	keys := make([]string, 0, len(specs))
	for key := range specs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fields := make([]MCPBundleConfigField, 0, len(keys))
	for _, key := range keys {
		spec := specs[key]
		def, err := expandMCPBundleDefault(spec.Default)
		if err != nil {
			return nil, fmt.Errorf("MCPB default for %s: %w", key, err)
		}
		title := strings.TrimSpace(spec.Title)
		if title == "" {
			title = strings.ReplaceAll(key, "_", " ")
		}
		fields = append(fields, MCPBundleConfigField{
			Key:         key,
			Type:        spec.Type,
			Title:       truncateUTF8(title, 200),
			Description: truncateUTF8(strings.TrimSpace(spec.Description), 1000),
			Required:    spec.Required,
			Multiple:    spec.Multiple,
			Sensitive:   spec.Sensitive,
			Default:     def,
			Min:         spec.Min,
			Max:         spec.Max,
		})
	}
	return fields, nil
}

func validateMCPBundleValues(specs map[string]mcpBundleConfigSpec, supplied map[string]any) (map[string]mcpBundleValue, error) {
	for key := range supplied {
		if _, ok := specs[key]; !ok {
			return nil, fmt.Errorf("unknown MCPB configuration field: %s", key)
		}
	}
	values := make(map[string]mcpBundleValue, len(specs))
	for key, spec := range specs {
		raw, present := supplied[key]
		if !present || raw == nil {
			raw = spec.Default
		}
		if raw == nil {
			if spec.Required {
				return nil, fmt.Errorf("%s is required", key)
			}
			values[key] = mcpBundleValue{}
			continue
		}
		value, err := normalizeMCPBundleValue(key, spec, raw)
		if err != nil {
			return nil, err
		}
		if spec.Required && value.scalar == "" && len(value.array) == 0 {
			return nil, fmt.Errorf("%s is required", key)
		}
		values[key] = value
	}
	return values, nil
}

func normalizeMCPBundleValue(key string, spec mcpBundleConfigSpec, raw any) (mcpBundleValue, error) {
	switch spec.Type {
	case "string":
		value, ok := raw.(string)
		if !ok {
			return mcpBundleValue{}, fmt.Errorf("%s must be text", key)
		}
		return boundedMCPBundleScalar(key, value)
	case "boolean":
		value, ok := raw.(bool)
		if !ok {
			return mcpBundleValue{}, fmt.Errorf("%s must be true or false", key)
		}
		return mcpBundleValue{scalar: strconv.FormatBool(value)}, nil
	case "number":
		value, ok := asFiniteFloat(raw)
		if !ok {
			return mcpBundleValue{}, fmt.Errorf("%s must be a finite number", key)
		}
		if spec.Min != nil && value < *spec.Min {
			return mcpBundleValue{}, fmt.Errorf("%s must be at least %g", key, *spec.Min)
		}
		if spec.Max != nil && value > *spec.Max {
			return mcpBundleValue{}, fmt.Errorf("%s must be at most %g", key, *spec.Max)
		}
		return mcpBundleValue{scalar: strconv.FormatFloat(value, 'g', -1, 64)}, nil
	case "directory", "file":
		if spec.Multiple {
			items, ok := asStringSlice(raw)
			if !ok || len(items) > maxMCPBundleMultipleValues {
				return mcpBundleValue{}, fmt.Errorf("%s must contain at most %d paths", key, maxMCPBundleMultipleValues)
			}
			out := make([]string, 0, len(items))
			for _, item := range items {
				checked, err := validateMCPBundlePathValue(key, spec.Type, item)
				if err != nil {
					return mcpBundleValue{}, err
				}
				out = append(out, checked)
			}
			return mcpBundleValue{array: out}, nil
		}
		value, ok := raw.(string)
		if !ok {
			return mcpBundleValue{}, fmt.Errorf("%s must be a path", key)
		}
		checked, err := validateMCPBundlePathValue(key, spec.Type, value)
		if err != nil {
			return mcpBundleValue{}, err
		}
		return mcpBundleValue{scalar: checked}, nil
	default:
		return mcpBundleValue{}, fmt.Errorf("unsupported MCPB configuration type for %s", key)
	}
}

func validateMCPBundlePathValue(key, kind, value string) (string, error) {
	expanded, err := expandMCPBundleString(value, "", nil)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	expanded = filepath.Clean(strings.TrimSpace(expanded))
	if expanded == "." || !filepath.IsAbs(expanded) || len(expanded) > maxMCPBundlePathBytes {
		return "", fmt.Errorf("%s must be an absolute path", key)
	}
	info, err := os.Stat(expanded)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	if kind == "directory" && !info.IsDir() {
		return "", fmt.Errorf("%s must be a directory", key)
	}
	if kind == "file" && !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s must be a regular file", key)
	}
	return expanded, nil
}

func boundedMCPBundleScalar(key, value string) (mcpBundleValue, error) {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > maxMCPBundleConfigValueBytes {
		return mcpBundleValue{}, fmt.Errorf("%s is too large or invalid", key)
	}
	return mcpBundleValue{scalar: value}, nil
}

func bundleServerConfig(manifest mcpBundleManifest, installDir string, values map[string]mcpBundleValue, enabled bool) (MCPServerConfig, error) {
	mcpConfig := manifest.Server.MCPConfig
	if patch, ok := mcpConfig.PlatformOverrides[mcpBundlePlatform()]; ok {
		if patch.Command != "" {
			mcpConfig.Command = patch.Command
		}
		if patch.Args != nil {
			mcpConfig.Args = patch.Args
		}
		if patch.Env != nil {
			if mcpConfig.Env == nil {
				mcpConfig.Env = map[string]string{}
			}
			for key, value := range patch.Env {
				mcpConfig.Env[key] = value
			}
		}
	}
	if strings.TrimSpace(mcpConfig.Command) == "" {
		switch manifest.Server.Type {
		case "node":
			mcpConfig.Command = "node"
			mcpConfig.Args = append([]string{"${__dirname}/" + manifest.Server.EntryPoint}, mcpConfig.Args...)
		case "python":
			mcpConfig.Command = "python3"
			mcpConfig.Args = append([]string{"${__dirname}/" + manifest.Server.EntryPoint}, mcpConfig.Args...)
		case "binary":
			mcpConfig.Command = "${__dirname}/" + manifest.Server.EntryPoint
		}
	}
	command, err := expandMCPBundleString(mcpConfig.Command, installDir, values)
	if err != nil {
		return MCPServerConfig{}, fmt.Errorf("resolve MCPB command: %w", err)
	}
	if manifest.Server.Type == "binary" && !filepath.IsAbs(command) && strings.ContainsAny(command, `/\`) {
		command = filepath.Join(installDir, filepath.FromSlash(command))
	}
	args := make([]string, 0, len(mcpConfig.Args))
	for _, raw := range mcpConfig.Args {
		if match := mcpUserConfigVarRE.FindStringSubmatch(raw); match != nil {
			value, ok := values[match[1]]
			if !ok {
				return MCPServerConfig{}, fmt.Errorf("unknown MCPB variable %s", raw)
			}
			if value.array != nil {
				args = append(args, value.array...)
				continue
			}
		}
		resolved, err := expandMCPBundleString(raw, installDir, values)
		if err != nil {
			return MCPServerConfig{}, fmt.Errorf("resolve MCPB argument: %w", err)
		}
		if path.Clean(strings.ReplaceAll(raw, `\`, "/")) == path.Clean(manifest.Server.EntryPoint) {
			resolved = filepath.Join(installDir, filepath.FromSlash(manifest.Server.EntryPoint))
		}
		args = append(args, resolved)
	}
	env := make(map[string]string, len(mcpConfig.Env))
	for key, raw := range mcpConfig.Env {
		resolved, err := expandMCPBundleString(raw, installDir, values)
		if err != nil {
			return MCPServerConfig{}, fmt.Errorf("resolve MCPB environment %s: %w", key, err)
		}
		env[key] = resolved
	}
	return validateMCPConfig(MCPServerConfig{
		Name:      manifest.Name,
		Transport: mcpTransportStdio,
		Command:   command,
		Args:      args,
		Env:       env,
		Enabled:   enabled,
	})
}

func expandMCPBundleDefault(value any) (any, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		return expandMCPBundleString(typed, "", nil)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("path-list defaults must contain strings")
			}
			expanded, err := expandMCPBundleString(text, "", nil)
			if err != nil {
				return nil, err
			}
			out = append(out, expanded)
		}
		return out, nil
	case bool, float64:
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported default value")
	}
}

func expandMCPBundleString(raw, installDir string, values map[string]mcpBundleValue) (string, error) {
	if !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) || len(raw) > maxMCPBundleConfigValueBytes {
		return "", fmt.Errorf("template value is too large or invalid")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	replacements := map[string]string{
		"${__dirname}":     installDir,
		"${HOME}":          home,
		"${DESKTOP}":       filepath.Join(home, "Desktop"),
		"${DOCUMENTS}":     filepath.Join(home, "Documents"),
		"${DOWNLOADS}":     filepath.Join(home, "Downloads"),
		"${pathSeparator}": string(filepath.Separator),
		"${/}":             string(filepath.Separator),
	}
	out := raw
	for variable, value := range replacements {
		out = strings.ReplaceAll(out, variable, value)
	}
	for key, value := range values {
		variable := "${user_config." + key + "}"
		if strings.Contains(out, variable) {
			if value.array != nil {
				return "", fmt.Errorf("%s can only be used as a complete command argument", variable)
			}
			out = strings.ReplaceAll(out, variable, value.scalar)
		}
	}
	if unresolved := mcpBundleVarRE.FindString(out); unresolved != "" {
		return "", fmt.Errorf("unresolved or unsupported variable %s", unresolved)
	}
	return out, nil
}

func extractMCPBundle(bundle *parsedMCPBundle) (string, error) {
	base := filepath.Join(configDir(), "mcp-bundles")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("create MCP bundle directory: %w", err)
	}
	slug := strings.ToLower(bundle.manifest.Name)
	target := filepath.Join(base, slug+"-"+bundle.manifest.Version+"-"+bundle.digest[:12])
	markerPath := filepath.Join(target, ".gokin-mcpb.sha256")
	if marker, err := os.ReadFile(markerPath); err == nil {
		if strings.TrimSpace(string(marker)) == bundle.digest {
			return target, nil
		}
		return "", fmt.Errorf("installed MCP bundle directory failed its digest marker check")
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect installed MCP bundle: %w", err)
	} else if _, statErr := os.Stat(target); statErr == nil {
		return "", fmt.Errorf("MCP bundle install target already exists without a valid marker")
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("inspect MCP bundle install target: %w", statErr)
	}

	staging, err := os.MkdirTemp(base, ".install-*")
	if err != nil {
		return "", fmt.Errorf("create MCP bundle staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	root, err := os.OpenRoot(staging)
	if err != nil {
		return "", err
	}
	defer root.Close()

	var written uint64
	for _, file := range bundle.entries {
		clean, err := cleanMCPBundleEntry(file.Name)
		if err != nil {
			return "", err
		}
		if file.Mode().IsDir() {
			if err := root.MkdirAll(filepath.FromSlash(clean), 0o700); err != nil {
				return "", fmt.Errorf("create MCP bundle directory %s: %w", clean, err)
			}
			continue
		}
		rel := filepath.FromSlash(clean)
		if err := root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
			return "", fmt.Errorf("create MCP bundle parent directory: %w", err)
		}
		source, err := file.Open()
		if err != nil {
			return "", fmt.Errorf("open MCP bundle entry %s: %w", clean, err)
		}
		perm := os.FileMode(0o600)
		if file.Mode().Perm()&0o111 != 0 {
			perm = 0o700
		}
		destination, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			_ = source.Close()
			return "", fmt.Errorf("create MCP bundle entry %s: %w", clean, err)
		}
		remaining := uint64(maxMCPBundleExpandedBytes) - written
		count, copyErr := io.Copy(destination, io.LimitReader(source, int64(remaining)+1))
		closeDstErr := destination.Close()
		closeSrcErr := source.Close()
		if copyErr != nil {
			return "", fmt.Errorf("extract MCP bundle entry %s: %w", clean, copyErr)
		}
		if closeDstErr != nil || closeSrcErr != nil {
			return "", fmt.Errorf("close MCP bundle entry %s", clean)
		}
		if count < 0 || uint64(count) > remaining {
			return "", fmt.Errorf("MCP bundle exceeds the %d MiB expanded-size limit", maxMCPBundleExpandedBytes>>20)
		}
		written += uint64(count)
	}
	if err := root.WriteFile(".gokin-mcpb.sha256", []byte(bundle.digest+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write MCP bundle marker: %w", err)
	}
	if err := root.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(staging, target); err != nil {
		if marker, readErr := os.ReadFile(markerPath); readErr == nil && strings.TrimSpace(string(marker)) == bundle.digest {
			return target, nil
		}
		return "", fmt.Errorf("commit MCP bundle install: %w", err)
	}
	return target, nil
}

func cleanMCPBundleEntry(name string) (string, error) {
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) || strings.Contains(name, `\`) ||
		len(name) == 0 || len(name) > maxMCPBundlePathBytes {
		return "", fmt.Errorf("invalid MCP bundle entry path")
	}
	clean := path.Clean(name)
	if clean == "." || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") ||
		strings.Contains(clean, ":") {
		return "", fmt.Errorf("MCP bundle entry escapes its install directory: %s", name)
	}
	return clean, nil
}

func readZipFileLimited(file *zip.File, limit int64) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return data, nil
}

func asFiniteFloat(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}

func asStringSlice(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		out := make([]string, len(typed))
		for i, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[i] = text
		}
		return out, true
	default:
		return nil, false
	}
}

func mcpBundlePlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	case "darwin":
		return "darwin"
	default:
		return runtime.GOOS
	}
}

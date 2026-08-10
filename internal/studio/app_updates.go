package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	updateAPIEndpoint      = "https://api.github.com/repos/ginkida/gokin-studio/releases/latest"
	updateReleaseURLPrefix = "https://github.com/ginkida/gokin-studio/releases/tag/v"
	updateCheckInterval    = 24 * time.Hour
	updateRequestTimeout   = 12 * time.Second
	updateResponseMaxBytes = 1 << 20
	updateStateMaxBytes    = 16 << 10
	updateStateSchema      = 1
)

type persistedUpdateState struct {
	Schema        int    `json:"schema"`
	CheckedAt     int64  `json:"checkedAt"`
	LatestVersion string `json:"latestVersion,omitempty"`
	PublishedAt   int64  `json:"publishedAt,omitempty"`
}

type githubLatestRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

func updateStatePath() string {
	return filepath.Join(configDir(), "update-state.json")
}

func updateNow(s *Studio) time.Time {
	if s.testUpdateNow != nil {
		return s.testUpdateNow().UTC()
	}
	return time.Now().UTC()
}

func updateEndpoint(s *Studio) string {
	if s.testUpdateEndpoint != "" {
		return s.testUpdateEndpoint
	}
	return updateAPIEndpoint
}

func updateHTTPClient(s *Studio) *http.Client {
	if s.testUpdateHTTPClient != nil {
		return s.testUpdateHTTPClient
	}
	return &http.Client{
		Timeout: updateRequestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many update-service redirects")
			}
			if req.URL.Scheme != "https" || req.URL.Hostname() != "api.github.com" || req.URL.Port() != "" {
				return errors.New("update service redirected outside api.github.com")
			}
			return nil
		},
	}
}

// GetUpdateStatus returns the last successful release check without touching
// the network. A missing or invalid cache is treated as "not checked yet";
// update metadata is disposable and must never prevent the app from opening.
func (s *Studio) GetUpdateStatus() *UpdateStatus {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	state, err := loadPersistedUpdateState()
	if err != nil {
		return &UpdateStatus{CurrentVersion: Version}
	}
	status := updateStatusFromState(state)
	return &status
}

// CheckForUpdates performs an explicit user-requested release check even when
// automatic checks are disabled or the daily cache is still fresh.
func (s *Studio) CheckForUpdates() (*UpdateStatus, error) {
	return s.checkForUpdates(true)
}

// CheckForUpdatesIfDue is called after the frontend event listeners are ready.
// It is a no-op when the user opted out or a successful check is under 24h old.
func (s *Studio) CheckForUpdatesIfDue() (*UpdateStatus, error) {
	return s.checkForUpdates(false)
}

func (s *Studio) checkForUpdates(force bool) (*UpdateStatus, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	now := updateNow(s)
	state, stateErr := loadPersistedUpdateState()
	if stateErr != nil && !os.IsNotExist(stateErr) {
		s.LogEvent("warn", "updates", fmt.Sprintf("discarded invalid update cache: %v", stateErr))
		state = persistedUpdateState{}
	}
	status := updateStatusFromState(state)

	if !force {
		s.mu.RLock()
		disabled := s.config != nil && s.config.Settings.AutoUpdateCheckDisabled
		s.mu.RUnlock()
		if disabled {
			return &status, nil
		}
		checkedAt := time.UnixMilli(state.CheckedAt)
		if state.CheckedAt > 0 && !checkedAt.After(now.Add(5*time.Minute)) && now.Sub(checkedAt) < updateCheckInterval {
			return &status, nil
		}
	}

	baseCtx := s.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, updateRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateEndpoint(s), nil)
	if err != nil {
		return nil, fmt.Errorf("create update request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Gokin-Studio/"+Version)

	resp, err := updateHTTPClient(s).Do(req)
	if err != nil {
		return nil, fmt.Errorf("check for updates: %w", err)
	}
	defer resp.Body.Close()

	next := persistedUpdateState{Schema: updateStateSchema, CheckedAt: now.UnixMilli()}
	if resp.StatusCode != http.StatusNotFound {
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
			return nil, fmt.Errorf("update service returned HTTP %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, updateResponseMaxBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read update response: %w", err)
		}
		if len(body) > updateResponseMaxBytes {
			return nil, fmt.Errorf("update response exceeds %d bytes", updateResponseMaxBytes)
		}
		var release githubLatestRelease
		if err := json.Unmarshal(body, &release); err != nil {
			return nil, fmt.Errorf("decode update response: %w", err)
		}
		if release.Draft || release.Prerelease {
			return nil, errors.New("update service returned a non-stable release")
		}
		latest, _, err := parseReleaseVersion(release.TagName)
		if err != nil {
			return nil, fmt.Errorf("invalid release tag: %w", err)
		}
		next.LatestVersion = latest
		if !release.PublishedAt.IsZero() {
			next.PublishedAt = release.PublishedAt.UTC().UnixMilli()
		}
	}
	if err := savePersistedUpdateState(next); err != nil {
		return nil, fmt.Errorf("save update status: %w", err)
	}
	status = updateStatusFromState(next)
	if status.Available {
		s.emitUpdateAvailable(status)
	}
	return &status, nil
}

func (s *Studio) emitUpdateAvailable(status UpdateStatus) {
	if s.testUpdateEmitter != nil {
		s.testUpdateEmitter(status)
		return
	}
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, EventUpdateAvailable, status)
	}
}

// ShowUpdateCenter is used by the native desktop menu. It restores the main
// workspace and asks React to open About and start a user-visible manual check
// after the settings surface has mounted.
func (s *Studio) ShowUpdateCenter() {
	s.activateStudioWindow()
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, EventOpenUpdateCenter)
	}
}

func updateStatusFromState(state persistedUpdateState) UpdateStatus {
	status := UpdateStatus{
		CurrentVersion: Version,
		LatestVersion:  state.LatestVersion,
		CheckedAt:      state.CheckedAt,
		PublishedAt:    state.PublishedAt,
	}
	if state.LatestVersion == "" {
		return status
	}
	_, currentParts, currentErr := parseReleaseVersion(Version)
	_, latestParts, latestErr := parseReleaseVersion(state.LatestVersion)
	if currentErr != nil || latestErr != nil {
		status.LatestVersion = ""
		status.PublishedAt = 0
		return status
	}
	status.ReleaseURL = updateReleaseURLPrefix + url.PathEscape(state.LatestVersion)
	status.Available = compareReleaseVersions(latestParts, currentParts) > 0
	return status
}

func loadPersistedUpdateState() (persistedUpdateState, error) {
	var state persistedUpdateState
	data, err := readRegularFileLimited(updateStatePath(), updateStateMaxBytes)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return persistedUpdateState{}, fmt.Errorf("decode update cache: %w", err)
	}
	if state.Schema != updateStateSchema {
		return persistedUpdateState{}, fmt.Errorf("unsupported update cache schema %d", state.Schema)
	}
	if state.CheckedAt < 0 || state.PublishedAt < 0 {
		return persistedUpdateState{}, errors.New("update cache contains an invalid timestamp")
	}
	if state.LatestVersion != "" {
		if _, _, err := parseReleaseVersion(state.LatestVersion); err != nil {
			return persistedUpdateState{}, fmt.Errorf("update cache contains an invalid version: %w", err)
		}
	}
	return state, nil
}

func savePersistedUpdateState(state persistedUpdateState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(updateStatePath(), append(data, '\n'), 0o600)
}

func parseReleaseVersion(raw string) (string, [3]uint64, error) {
	var parsed [3]uint64
	value := strings.TrimSpace(raw)
	if strings.HasPrefix(value, "v") {
		value = value[1:]
	}
	parts := strings.Split(value, ".")
	if len(parts) != len(parsed) {
		return "", parsed, fmt.Errorf("%q is not MAJOR.MINOR.PATCH", raw)
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return "", parsed, fmt.Errorf("%q is not canonical semver", raw)
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return "", parsed, fmt.Errorf("%q is not a stable semver", raw)
			}
		}
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return "", parsed, fmt.Errorf("%q is out of range", raw)
		}
		parsed[i] = n
	}
	return value, parsed, nil
}

func compareReleaseVersions(a, b [3]uint64) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

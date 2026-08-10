package studio

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	previewSessionProfileMaxBytes        = 512 << 10
	previewSessionLocalStorageMaxBytes   = 256 << 10
	previewSessionLocalStorageMaxItems   = 512
	previewSessionCookieMaxItems         = 128
	previewSessionStoragePayloadMaxBytes = 320 << 10
)

type PreviewSessionPersistenceStatus struct {
	Enabled             bool  `json:"enabled"`
	HasData             bool  `json:"hasData"`
	LocalStorageEntries int   `json:"localStorageEntries"`
	Cookies             int   `json:"cookies"`
	UpdatedAt           int64 `json:"updatedAt,omitempty"`
}

type previewPersistedCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Path     string `json:"path,omitempty"`
	Expires  int64  `json:"expires,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
	SameSite int    `json:"sameSite,omitempty"`
}

type previewSessionProfile struct {
	Version      int                      `json:"version"`
	Enabled      bool                     `json:"enabled"`
	LocalStorage map[string]string        `json:"localStorage,omitempty"`
	Cookies      []previewPersistedCookie `json:"cookies,omitempty"`
	UpdatedAt    int64                    `json:"updatedAt,omitempty"`
}

type previewSessionProfileState struct {
	mu   sync.Mutex
	path string
	data previewSessionProfile
}

type previewStorageSnapshot struct {
	LocalStorage map[string]string `json:"localStorage"`
	Cookies      string            `json:"cookies"`
}

type previewStorageBootstrap struct {
	Enabled      bool              `json:"enabled"`
	LocalStorage map[string]string `json:"localStorage,omitempty"`
}

func previewSessionProfilesDir() string {
	return filepath.Join(configDir(), "preview-sessions")
}

func previewSessionProfilePath(projectID, sessionID, configuration string) string {
	name := projectSessionStorageKey(projectID, sessionID) + "_" + safeStorageKey(configuration) + ".json"
	return filepath.Join(previewSessionProfilesDir(), name)
}

func loadPreviewSessionProfile(projectID, sessionID, configuration string) (*previewSessionProfileState, error) {
	path := previewSessionProfilePath(projectID, sessionID, configuration)
	state := &previewSessionProfileState{path: path, data: previewSessionProfile{Version: 1}}
	data, err := readRegularFileLimited(path, previewSessionProfileMaxBytes)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read preview session profile: %w", err)
	}
	if !utf8.Valid(data) || json.Unmarshal(data, &state.data) != nil || state.data.Version != 1 {
		return nil, fmt.Errorf("preview session profile is invalid")
	}
	if err := validatePreviewLocalStorage(state.data.LocalStorage); err != nil {
		return nil, fmt.Errorf("preview session profile is invalid: %w", err)
	}
	state.data.Cookies = sanitizePersistedCookies(state.data.Cookies)
	return state, nil
}

func validatePreviewLocalStorage(storage map[string]string) error {
	if len(storage) > previewSessionLocalStorageMaxItems {
		return fmt.Errorf("local storage has too many entries")
	}
	total := 0
	for key, value := range storage {
		if key == "" || !utf8.ValidString(key) || !utf8.ValidString(value) || len(key) > 4096 || len(value) > 128<<10 {
			return fmt.Errorf("local storage contains an invalid entry")
		}
		total += len(key) + len(value)
		if total > previewSessionLocalStorageMaxBytes {
			return fmt.Errorf("local storage exceeds the %d KiB limit", previewSessionLocalStorageMaxBytes>>10)
		}
	}
	return nil
}

func sanitizePersistedCookies(cookies []previewPersistedCookie) []previewPersistedCookie {
	now := time.Now().Unix()
	seen := make(map[string]previewPersistedCookie)
	for _, cookie := range cookies {
		if cookie.Name == "" || strings.HasPrefix(cookie.Name, "__gokin_") || len(cookie.Name) > 256 || len(cookie.Value) > 4096 ||
			!utf8.ValidString(cookie.Name) || !utf8.ValidString(cookie.Value) || strings.ContainsAny(cookie.Name, "\r\n;= \t") {
			continue
		}
		if cookie.Expires > 0 && cookie.Expires <= now {
			continue
		}
		if cookie.Path == "" || cookie.Path[0] != '/' || len(cookie.Path) > 1024 || strings.ContainsAny(cookie.Path, "\r\n") {
			cookie.Path = "/"
		}
		key := cookie.Name + "\x00" + cookie.Path
		seen[key] = cookie
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > previewSessionCookieMaxItems {
		keys = keys[:previewSessionCookieMaxItems]
	}
	result := make([]previewPersistedCookie, 0, len(keys))
	for _, key := range keys {
		result = append(result, seen[key])
	}
	return result
}

func (s *previewSessionProfileState) status() *PreviewSessionPersistenceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &PreviewSessionPersistenceStatus{
		Enabled: s.data.Enabled, HasData: len(s.data.LocalStorage) > 0 || len(s.data.Cookies) > 0,
		LocalStorageEntries: len(s.data.LocalStorage), Cookies: len(s.data.Cookies), UpdatedAt: s.data.UpdatedAt,
	}
}

func (s *previewSessionProfileState) saveLocked() error {
	if !s.data.Enabled {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	s.data.Version = 1
	s.data.UpdatedAt = time.Now().UnixMilli()
	data, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	if len(data) > previewSessionProfileMaxBytes {
		return fmt.Errorf("preview session profile exceeds the storage limit")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(s.path, data, 0o600)
}

func (s *previewSessionProfileState) setEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Enabled = enabled
	if !enabled {
		s.data.LocalStorage = nil
		s.data.Cookies = nil
		s.data.UpdatedAt = 0
	}
	return s.saveLocked()
}

func (s *previewSessionProfileState) clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.LocalStorage = nil
	s.data.Cookies = nil
	return s.saveLocked()
}

func (s *previewSessionProfileState) bootstrap() previewStorageBootstrap {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := previewStorageBootstrap{Enabled: s.data.Enabled}
	if s.data.Enabled && len(s.data.LocalStorage) > 0 {
		result.LocalStorage = make(map[string]string, len(s.data.LocalStorage))
		for key, value := range s.data.LocalStorage {
			result.LocalStorage[key] = value
		}
	}
	return result
}

func cookieKey(name, path string) string {
	if path == "" || path[0] != '/' {
		path = "/"
	}
	return name + "\x00" + path
}

// cookiePathMatches implements the path boundary rule from RFC 6265. A
// cookie scoped to /admin is valid for /admin and /admin/users, but must not
// be sent to /administrator merely because the strings share a prefix.
func cookiePathMatches(requestPath, cookiePath string) bool {
	if requestPath == "" || requestPath[0] != '/' {
		requestPath = "/"
	}
	if cookiePath == "" || cookiePath[0] != '/' {
		cookiePath = "/"
	}
	if requestPath == cookiePath {
		return true
	}
	if !strings.HasPrefix(requestPath, cookiePath) {
		return false
	}
	return strings.HasSuffix(cookiePath, "/") || (len(requestPath) > len(cookiePath) && requestPath[len(cookiePath)] == '/')
}

func persistedCookieFromHTTP(cookie *http.Cookie) (previewPersistedCookie, bool) {
	if cookie == nil || cookie.Name == "" || strings.HasPrefix(cookie.Name, "__gokin_") || len(cookie.Name) > 256 || len(cookie.Value) > 4096 ||
		strings.ContainsAny(cookie.Name, "\r\n;= \t") || !utf8.ValidString(cookie.Value) {
		return previewPersistedCookie{}, false
	}
	path := cookie.Path
	if path == "" || path[0] != '/' || len(path) > 1024 || strings.ContainsAny(path, "\r\n") {
		path = "/"
	}
	expires := int64(0)
	if cookie.MaxAge > 0 {
		expires = time.Now().Add(time.Duration(cookie.MaxAge) * time.Second).Unix()
	} else if !cookie.Expires.IsZero() {
		expires = cookie.Expires.Unix()
	}
	return previewPersistedCookie{Name: cookie.Name, Value: cookie.Value, Path: path, Expires: expires, Secure: cookie.Secure, HTTPOnly: cookie.HttpOnly, SameSite: int(cookie.SameSite)}, true
}

func (s *previewSessionProfileState) mergeCookies(cookies []*http.Cookie) error {
	if len(cookies) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := make(map[string]previewPersistedCookie, len(s.data.Cookies))
	for _, cookie := range sanitizePersistedCookies(s.data.Cookies) {
		current[cookieKey(cookie.Name, cookie.Path)] = cookie
	}
	changed := false
	for _, cookie := range cookies {
		persisted, ok := persistedCookieFromHTTP(cookie)
		if !ok {
			continue
		}
		key := cookieKey(persisted.Name, persisted.Path)
		deleteCookie := cookie.MaxAge < 0 || (persisted.Expires > 0 && persisted.Expires <= time.Now().Unix())
		if deleteCookie {
			if _, exists := current[key]; exists {
				delete(current, key)
				changed = true
			}
			continue
		}
		if existing, exists := current[key]; !exists || existing != persisted {
			current[key] = persisted
			changed = true
		}
	}
	if !changed {
		return nil
	}
	next := make([]previewPersistedCookie, 0, len(current))
	for _, cookie := range current {
		next = append(next, cookie)
	}
	s.data.Cookies = sanitizePersistedCookies(next)
	if s.data.Enabled {
		return s.saveLocked()
	}
	return nil
}

func (s *previewSessionProfileState) requestCookies(path string) []*http.Cookie {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*http.Cookie, 0, len(s.data.Cookies))
	for _, cookie := range sanitizePersistedCookies(s.data.Cookies) {
		if !cookiePathMatches(path, cookie.Path) {
			continue
		}
		result = append(result, &http.Cookie{Name: cookie.Name, Value: cookie.Value, Path: cookie.Path})
	}
	return result
}

func (s *previewSessionProfileState) responseCookies() []*http.Cookie {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*http.Cookie, 0, len(s.data.Cookies))
	for _, cookie := range sanitizePersistedCookies(s.data.Cookies) {
		item := &http.Cookie{Name: cookie.Name, Value: cookie.Value, Path: cookie.Path, Secure: cookie.Secure, HttpOnly: cookie.HTTPOnly, SameSite: http.SameSite(cookie.SameSite)}
		if cookie.Expires > 0 {
			item.Expires = time.Unix(cookie.Expires, 0)
		}
		result = append(result, item)
	}
	return result
}

func (s *previewSessionProfileState) saveBrowserSnapshot(snapshot previewStorageSnapshot) error {
	if err := validatePreviewLocalStorage(snapshot.LocalStorage); err != nil {
		return err
	}
	s.mu.Lock()
	if !s.data.Enabled {
		s.mu.Unlock()
		return nil
	}
	s.data.LocalStorage = make(map[string]string, len(snapshot.LocalStorage))
	for key, value := range snapshot.LocalStorage {
		s.data.LocalStorage[key] = value
	}
	s.mu.Unlock()
	if strings.TrimSpace(snapshot.Cookies) != "" {
		request := &http.Request{Header: make(http.Header)}
		request.Header.Set("Cookie", snapshot.Cookies)
		if err := s.mergeCookies(request.Cookies()); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Studio) validatePreviewProfileTarget(projectID, sessionID, configuration string) error {
	if _, _, err := s.projectSession(projectID, sessionID); err != nil {
		return err
	}
	config, err := s.GetSessionPreviewConfig(projectID, sessionID)
	if err != nil {
		return err
	}
	for _, candidate := range config.Configurations {
		if candidate.Name == configuration {
			return nil
		}
	}
	return fmt.Errorf("preview configuration not found: %s", configuration)
}

func (s *Studio) previewProfile(projectID, sessionID, configuration string) (*previewSessionProfileState, error) {
	key := previewServerKey(projectID, sessionID, configuration)
	s.previewMu.Lock()
	run := s.previewServers[key]
	s.previewMu.Unlock()
	if run != nil && run.profile != nil {
		return run.profile, nil
	}
	return loadPreviewSessionProfile(projectID, sessionID, configuration)
}

func (s *Studio) GetPreviewSessionPersistence(projectID, sessionID, configuration string) (*PreviewSessionPersistenceStatus, error) {
	if err := s.validatePreviewProfileTarget(projectID, sessionID, configuration); err != nil {
		return nil, err
	}
	profile, err := s.previewProfile(projectID, sessionID, configuration)
	if err != nil {
		return nil, err
	}
	return profile.status(), nil
}

func (s *Studio) SetPreviewSessionPersistence(projectID, sessionID, configuration string, enabled bool) (*PreviewSessionPersistenceStatus, error) {
	if err := s.validatePreviewProfileTarget(projectID, sessionID, configuration); err != nil {
		return nil, err
	}
	profile, err := s.previewProfile(projectID, sessionID, configuration)
	if err != nil {
		return nil, err
	}
	if err := profile.setEnabled(enabled); err != nil {
		return nil, fmt.Errorf("update preview session persistence: %w", err)
	}
	return profile.status(), nil
}

func (s *Studio) ClearPreviewSessionData(projectID, sessionID, configuration string) (*PreviewSessionPersistenceStatus, error) {
	if err := s.validatePreviewProfileTarget(projectID, sessionID, configuration); err != nil {
		return nil, err
	}
	profile, err := s.previewProfile(projectID, sessionID, configuration)
	if err != nil {
		return nil, err
	}
	if err := profile.clear(); err != nil {
		return nil, fmt.Errorf("clear preview session data: %w", err)
	}
	return profile.status(), nil
}

func (s *Studio) SavePreviewBrowserStorage(projectID, sessionID, configuration, bridgeToken, payload string) error {
	if len(payload) > previewSessionStoragePayloadMaxBytes || !utf8.ValidString(payload) {
		return fmt.Errorf("preview storage snapshot exceeds the allowed size")
	}
	key := previewServerKey(projectID, sessionID, configuration)
	s.previewMu.Lock()
	run := s.previewServers[key]
	s.previewMu.Unlock()
	if run == nil {
		return fmt.Errorf("preview server is not running")
	}
	run.mu.RLock()
	valid := run.state == "running" && run.bridgeToken == bridgeToken && run.projectID == projectID && run.sessionID == sessionID
	profile := run.profile
	run.mu.RUnlock()
	if !valid || profile == nil {
		return fmt.Errorf("preview storage bridge is stale")
	}
	var snapshot previewStorageSnapshot
	if json.Unmarshal([]byte(payload), &snapshot) != nil {
		return fmt.Errorf("preview storage snapshot is invalid JSON")
	}
	if err := profile.saveBrowserSnapshot(snapshot); err != nil {
		return fmt.Errorf("save preview session data: %w", err)
	}
	return nil
}

func removePreviewSessionProfiles(projectID, sessionID string) {
	dir := previewSessionProfilesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := safeStorageKey(projectID) + "_"
	if sessionID != "" {
		prefix = projectSessionStorageKey(projectID, sessionID) + "_"
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".json") {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

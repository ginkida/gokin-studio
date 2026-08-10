package studio

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	staticPreviewConfiguration = "__gokin_static_file__"
	staticPreviewQueryToken    = "gokin_static_token"
	staticPreviewCookie        = "__gokin_static"
	staticPreviewDocumentMax   = 4 << 20
	staticPreviewAssetMax      = 64 << 20
	staticPreviewWrapperPath   = "/__gokin_static_preview__"
)

type staticPreviewFileType struct {
	mimeType string
	maxBytes int64
	entry    bool
}

var staticPreviewFileTypes = map[string]staticPreviewFileType{
	".html":        {"text/html; charset=utf-8", staticPreviewDocumentMax, true},
	".htm":         {"text/html; charset=utf-8", staticPreviewDocumentMax, true},
	".pdf":         {"application/pdf", 30 << 20, true},
	".svg":         {"image/svg+xml", 8 << 20, true},
	".png":         {"image/png", 30 << 20, true},
	".jpg":         {"image/jpeg", 30 << 20, true},
	".jpeg":        {"image/jpeg", 30 << 20, true},
	".gif":         {"image/gif", 30 << 20, true},
	".webp":        {"image/webp", 30 << 20, true},
	".avif":        {"image/avif", 30 << 20, true},
	".bmp":         {"image/bmp", 30 << 20, true},
	".tif":         {"image/tiff", 30 << 20, true},
	".tiff":        {"image/tiff", 30 << 20, true},
	".heic":        {"image/heic", 30 << 20, true},
	".heif":        {"image/heif", 30 << 20, true},
	".ico":         {"image/x-icon", 8 << 20, true},
	".mp4":         {"video/mp4", staticPreviewAssetMax, true},
	".webm":        {"video/webm", staticPreviewAssetMax, true},
	".ogv":         {"video/ogg", staticPreviewAssetMax, true},
	".mov":         {"video/quicktime", staticPreviewAssetMax, true},
	".m4v":         {"video/x-m4v", staticPreviewAssetMax, true},
	".css":         {"text/css; charset=utf-8", staticPreviewDocumentMax, false},
	".js":          {"text/javascript; charset=utf-8", staticPreviewDocumentMax, false},
	".mjs":         {"text/javascript; charset=utf-8", staticPreviewDocumentMax, false},
	".cjs":         {"text/javascript; charset=utf-8", staticPreviewDocumentMax, false},
	".json":        {"application/json; charset=utf-8", staticPreviewDocumentMax, false},
	".webmanifest": {"application/manifest+json; charset=utf-8", staticPreviewDocumentMax, false},
	".xml":         {"application/xml; charset=utf-8", staticPreviewDocumentMax, false},
	".txt":         {"text/plain; charset=utf-8", staticPreviewDocumentMax, false},
	".map":         {"application/json; charset=utf-8", staticPreviewDocumentMax, false},
	".wasm":        {"application/wasm", 16 << 20, false},
	".woff":        {"font/woff", 16 << 20, false},
	".woff2":       {"font/woff2", 16 << 20, false},
	".ttf":         {"font/ttf", 16 << 20, false},
	".otf":         {"font/otf", 16 << 20, false},
	".eot":         {"application/vnd.ms-fontobject", 16 << 20, false},
	".mp3":         {"audio/mpeg", staticPreviewAssetMax, false},
	".wav":         {"audio/wav", staticPreviewAssetMax, false},
	".ogg":         {"audio/ogg", staticPreviewAssetMax, false},
}

func staticPreviewType(name string) (staticPreviewFileType, bool) {
	value, ok := staticPreviewFileTypes[strings.ToLower(filepath.Ext(name))]
	return value, ok
}

func staticPreviewBrowserPath(name string) string {
	if fileType, ok := staticPreviewType(name); ok && !strings.HasPrefix(fileType.mimeType, "text/html") {
		return staticPreviewWrapperPath
	}
	return "/" + url.PathEscape(filepath.Base(name))
}

func staticPreviewAccessHandler(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Referrer-Policy", "no-referrer")
		cookie, _ := request.Cookie(staticPreviewCookie)
		cookieValid := cookie != nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) == 1
		queryToken := request.URL.Query().Get(staticPreviewQueryToken)
		queryValid := subtle.ConstantTimeCompare([]byte(queryToken), []byte(token)) == 1
		if queryValid {
			if !cookieValid {
				http.SetCookie(response, &http.Cookie{
					Name: staticPreviewCookie, Value: token, Path: "/", HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})
			}
			redirect := *request.URL
			values := redirect.Query()
			values.Del(staticPreviewQueryToken)
			redirect.RawQuery = values.Encode()
			http.Redirect(response, request, redirect.String(), http.StatusSeeOther)
			return
		}
		if !cookieValid {
			http.NotFound(response, request)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func staticPreviewCSP() string {
	return strings.Join([]string{
		"default-src 'self' data: blob:",
		"script-src 'self' 'unsafe-inline' blob:",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"media-src 'self' data: blob:",
		"connect-src 'self'",
		"worker-src 'self' blob:",
		"frame-src 'self' data: blob:",
		"object-src 'self'",
		"base-uri 'self'",
		"form-action 'self'",
	}, "; ")
}

func staticPreviewHandler(root *os.Root, baseDir, accessToken, entryName string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if subtle.ConstantTimeCompare([]byte(request.Header.Get("X-Gokin-Static-Token")), []byte(accessToken)) != 1 {
			http.NotFound(response, request)
			return
		}
		if request.URL.Path == staticPreviewWrapperPath {
			fileType, ok := staticPreviewType(entryName)
			if !ok || !fileType.entry || strings.HasPrefix(fileType.mimeType, "text/html") {
				http.NotFound(response, request)
				return
			}
			asset := url.PathEscape(entryName)
			label := html.EscapeString(entryName)
			viewer := `<img src="./` + asset + `" alt="` + label + `">`
			if strings.HasPrefix(fileType.mimeType, "application/pdf") {
				viewer = `<embed src="./` + asset + `" type="application/pdf" title="` + label + `">`
			} else if strings.HasPrefix(fileType.mimeType, "video/") {
				viewer = `<video src="./` + asset + `" controls autoplay title="` + label + `"></video>`
			}
			body := []byte(`<!doctype html><html><head><meta charset="utf-8"><title>` + label + `</title><style>html,body{width:100%;height:100%;margin:0;background:#111;color:#eee}body{display:grid;place-items:center;overflow:auto}img,video{display:block;max-width:100%;max-height:100%;object-fit:contain}embed{display:block;width:100%;height:100%;border:0}</style></head><body>` + viewer + `</body></html>`)
			response.Header().Set("Content-Type", "text/html; charset=utf-8")
			response.Header().Set("Content-Security-Policy", staticPreviewCSP())
			response.Header().Set("Cache-Control", "no-store")
			response.Header().Set("Referrer-Policy", "no-referrer")
			response.Header().Set("X-Content-Type-Options", "nosniff")
			http.ServeContent(response, request, "preview.html", time.Time{}, bytes.NewReader(body))
			return
		}
		raw := strings.TrimPrefix(request.URL.Path, "/")
		if raw == "" {
			raw = entryName
		}
		if strings.Contains(raw, "\\") {
			http.NotFound(response, request)
			return
		}
		clean := path.Clean("/" + raw)
		if clean == "/" || strings.Contains(clean, "\x00") {
			http.NotFound(response, request)
			return
		}
		assetRel := filepath.FromSlash(strings.TrimPrefix(clean, "/"))
		rel := assetRel
		if baseDir != "." && baseDir != "" {
			rel = filepath.Join(baseDir, assetRel)
		}
		rel, err := normalizeProjectSubPath(filepath.ToSlash(rel))
		if err != nil {
			http.NotFound(response, request)
			return
		}
		fileType, ok := staticPreviewType(rel)
		if !ok {
			http.NotFound(response, request)
			return
		}
		info, err := root.Stat(rel)
		if err != nil || !info.Mode().IsRegular() || info.Size() > fileType.maxBytes {
			http.NotFound(response, request)
			return
		}
		file, err := root.Open(rel)
		if err != nil {
			http.NotFound(response, request)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, fileType.maxBytes+1))
		if err != nil || int64(len(data)) > fileType.maxBytes {
			http.Error(response, "preview asset unavailable", http.StatusInternalServerError)
			return
		}
		mimeType := fileType.mimeType
		if mimeType == "" {
			mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(rel)))
		}
		response.Header().Set("Content-Type", mimeType)
		response.Header().Set("Content-Security-Policy", staticPreviewCSP())
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(response, request, filepath.Base(rel), info.ModTime(), bytes.NewReader(data))
	})
}

func closeStaticPreviewResources(run *previewServerRun) {
	if run == nil {
		return
	}
	if run.staticTarget != nil {
		_ = run.staticTarget.Close()
		run.staticTarget = nil
	}
	if run.staticRoot != nil {
		_ = run.staticRoot.Close()
		run.staticRoot = nil
	}
}

// OpenSessionPreviewFile serves one static HTML/PDF/image/video entry from the
// selected chat worktree. Assets are restricted to the entry's directory,
// symlink resolution remains anchored by os.Root, and a random HttpOnly token
// prevents another local process from using the loopback server as a file API.
func (s *Studio) OpenSessionPreviewFile(projectID, sessionID, subPath string) (*PreviewServerStatus, error) {
	project, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	workDir, err := sessionWorkingDirectory(project, session)
	if err != nil {
		return nil, err
	}
	root, rel, err := openProjectPath(workDir, subPath)
	if err != nil {
		return nil, err
	}
	fileType, supported := staticPreviewType(rel)
	if !supported || !fileType.entry {
		_ = root.Close()
		return nil, fmt.Errorf("static preview supports HTML, PDF, image, and video files")
	}
	info, err := root.Stat(rel)
	if err != nil || !info.Mode().IsRegular() {
		_ = root.Close()
		return nil, fmt.Errorf("preview file is not a regular file")
	}
	if info.Size() > fileType.maxBytes {
		_ = root.Close()
		return nil, fmt.Errorf("preview file exceeds the %d MiB limit", fileType.maxBytes>>20)
	}
	key := previewServerKey(projectID, sessionID, staticPreviewConfiguration)
	s.previewMu.Lock()
	if s.previewStaticEpoch == nil {
		s.previewStaticEpoch = make(map[string]uint64)
	}
	s.previewStaticEpoch[key]++
	epoch := s.previewStaticEpoch[key]
	previous := s.previewServers[key]
	delete(s.previewServers, key)
	s.previewMu.Unlock()
	if previous != nil {
		previous.mu.Lock()
		previous.state = "stopped"
		if previous.proxy != nil {
			_ = previous.proxy.Close()
		}
		closeStaticPreviewResources(previous)
		previous.mu.Unlock()
	}
	bridgeToken, err := newPreviewBridgeToken()
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	accessToken, err := newPreviewBridgeToken()
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("start static preview: %w", err)
	}
	baseDir := filepath.Dir(rel)
	entryName := filepath.Base(rel)
	target := &http.Server{
		Handler:           staticPreviewHandler(root, baseDir, accessToken, entryName),
		ReadHeaderTimeout: 5 * time.Second,
	}
	run := &previewServerRun{
		projectID: projectID, sessionID: sessionID,
		config: PreviewServerConfiguration{Name: staticPreviewConfiguration},
		state:  "running", startedAt: time.Now().UnixMilli(), targetURL: "http://" + listener.Addr().String(),
		bridgeToken: bridgeToken, autoVerify: true, staticPath: filepath.ToSlash(rel),
		staticRoot: root, staticTarget: target, staticAccessToken: accessToken,
	}
	go func() { _ = target.Serve(listener) }()
	if err := startPreviewBridgeProxy(run); err != nil {
		_ = listener.Close()
		closeStaticPreviewResources(run)
		return nil, fmt.Errorf("start static preview bridge: %w", err)
	}
	run.browserURL += staticPreviewBrowserPath(entryName) + "?" + staticPreviewQueryToken + "=" + url.QueryEscape(accessToken)

	s.previewMu.Lock()
	if s.previewStaticEpoch[key] != epoch {
		s.previewMu.Unlock()
		if run.proxy != nil {
			_ = run.proxy.Close()
		}
		closeStaticPreviewResources(run)
		return nil, fmt.Errorf("static preview request was superseded")
	}
	previous = s.previewServers[key]
	s.previewServers[key] = run
	s.previewMu.Unlock()
	if previous != nil {
		previous.mu.Lock()
		previous.state = "stopped"
		if previous.proxy != nil {
			_ = previous.proxy.Close()
		}
		closeStaticPreviewResources(previous)
		previous.mu.Unlock()
	}
	return previewStatus(run), nil
}

func (s *Studio) CloseSessionPreviewFile(projectID, sessionID, bridgeToken string) error {
	if _, _, err := s.projectSession(projectID, sessionID); err != nil {
		return err
	}
	key := previewServerKey(projectID, sessionID, staticPreviewConfiguration)
	s.previewMu.Lock()
	if s.previewStaticEpoch == nil {
		s.previewStaticEpoch = make(map[string]uint64)
	}
	s.previewStaticEpoch[key]++
	run := s.previewServers[key]
	if run != nil {
		run.mu.RLock()
		matches := bridgeToken != "" && run.bridgeToken == bridgeToken
		run.mu.RUnlock()
		if !matches {
			run = nil
		} else {
			delete(s.previewServers, key)
		}
	}
	s.previewMu.Unlock()
	if run == nil {
		return nil
	}
	run.mu.Lock()
	run.state = "stopped"
	if run.proxy != nil {
		_ = run.proxy.Close()
	}
	closeStaticPreviewResources(run)
	run.mu.Unlock()
	return nil
}

func (s *Studio) GetSessionPreviewFileStatus(projectID, sessionID string) (*PreviewServerStatus, error) {
	if _, _, err := s.projectSession(projectID, sessionID); err != nil {
		return nil, err
	}
	key := previewServerKey(projectID, sessionID, staticPreviewConfiguration)
	s.previewMu.Lock()
	run := s.previewServers[key]
	s.previewMu.Unlock()
	if run == nil {
		return &PreviewServerStatus{Configuration: staticPreviewConfiguration, State: "stopped"}, nil
	}
	return previewStatus(run), nil
}

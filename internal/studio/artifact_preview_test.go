package studio

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func TestXLSXSharedStringsPreserveItemBoundaries(t *testing.T) {
	var packageBytes bytes.Buffer
	writer := zip.NewWriter(&packageBytes)
	part, err := writer.Create("xl/sharedStrings.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(
		`<?xml version="1.0" encoding="UTF-8"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
			`<si><t>First</t></si><si><r><t>Rich</t></r><r><t> text</t></r></si></sst>`,
	)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(packageBytes.Bytes()), int64(packageBytes.Len()))
	if err != nil {
		t.Fatal(err)
	}
	values, err := xlsxSharedStrings(reader.File)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0] != "First" || values[1] != "Rich text" {
		t.Fatalf("shared strings = %#v", values)
	}
}

func TestReadArtifactContentHTMLAndSVG(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Artifacts")
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	htmlPath := filepath.Join(project.Directory, "dashboard.html")
	svgPath := filepath.Join(project.Directory, "chart.svg")
	if err := os.WriteFile(htmlPath, []byte("<!doctype html><h1>Hello</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svgPath, []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o600); err != nil {
		t.Fatal(err)
	}
	html, err := s.ReadArtifactContent(info.ID, "dashboard.html")
	if err != nil {
		t.Fatal(err)
	}
	if html.MIMEType != "text/html" || html.Content != "<!doctype html><h1>Hello</h1>" ||
		html.Path != "dashboard.html" || html.ModifiedAt == 0 {
		t.Fatalf("HTML artifact = %#v", html)
	}
	svg, err := s.ReadArtifactContent(info.ID, "chart.svg")
	if err != nil {
		t.Fatal(err)
	}
	if svg.MIMEType != "image/svg+xml" {
		t.Fatalf("SVG artifact = %#v", svg)
	}
}

func TestListProjectArtifactsDiscoversMetadataAndVersions(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Artifact library")
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	if err := os.MkdirAll(filepath.Join(project.Directory, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project.Directory, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project.Directory, "node_modules", "noise"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"dashboard.html":                 []byte("<h1>Dashboard v1</h1>"),
		"reports/brief.pdf":              []byte("%PDF-1.4\n"),
		"dist/generated.svg":             []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		"notes.txt":                      []byte("not an artifact"),
		"node_modules/noise/hidden.html": []byte("<p>noise</p>"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(project.Directory, filepath.FromSlash(name)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SaveArtifactVersion(info.ID, "dashboard.html"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project.Directory, "dashboard.html"), []byte("<h1>Dashboard v2</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveArtifactVersion(info.ID, "dashboard.html"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(project.Directory, "dashboard.html"),
		filepath.Join(project.Directory, "linked.html"),
	); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}

	result, err := s.ListProjectArtifacts(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Truncated || len(result.Artifacts) != 3 {
		t.Fatalf("artifact library = %#v", result)
	}
	byPath := make(map[string]ArtifactSummary, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		byPath[artifact.Path] = artifact
	}
	dashboard := byPath["dashboard.html"]
	if dashboard.PreviewKind != "web" || dashboard.MIMEType != "text/html" ||
		dashboard.VersionCount != 2 || dashboard.LatestVersionAt == 0 || !dashboard.Previewable {
		t.Fatalf("dashboard summary = %#v", dashboard)
	}
	pdf := byPath["reports/brief.pdf"]
	if pdf.PreviewKind != "pdf" || pdf.Directory != "reports" || !pdf.Previewable {
		t.Fatalf("PDF summary = %#v", pdf)
	}
	if _, ok := byPath["dist/generated.svg"]; !ok {
		t.Fatal("generated dist artifact was omitted")
	}
	for _, omitted := range []string{"notes.txt", "node_modules/noise/hidden.html", "linked.html"} {
		if _, ok := byPath[omitted]; ok {
			t.Fatalf("unsafe/unsupported path %q was listed", omitted)
		}
	}
}

func TestListProjectArtifactsMarksOversizeAndValidatesProject(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Artifact limits")
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	oversize := make([]byte, artifactPreviewMaxBytes+1)
	if err := os.WriteFile(filepath.Join(project.Directory, "too-large.html"), oversize, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := s.ListProjectArtifacts(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Previewable ||
		!strings.Contains(result.Artifacts[0].Issue, "2 MiB") {
		t.Fatalf("oversize summary = %#v", result)
	}
	if _, err := s.ListProjectArtifacts(""); err == nil {
		t.Fatal("empty project ID was accepted")
	}
	if _, err := s.ListProjectArtifacts("missing"); err == nil {
		t.Fatal("unknown project was accepted")
	}
}

func TestReadArtifactContentRejectsUnsupportedEscapeAndOversize(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Artifact limits")
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	if err := os.WriteFile(filepath.Join(project.Directory, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadArtifactContent(info.ID, "notes.txt"); err == nil || !strings.Contains(err.Error(), "DOCX") {
		t.Fatalf("unsupported error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.html")
	if err := os.WriteFile(outside, []byte("<p>secret</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project.Directory, "escape.html")); err == nil {
		if _, err := s.ReadArtifactContent(info.ID, "escape.html"); err == nil {
			t.Fatal("artifact preview followed an outward symlink")
		}
	}
	large := strings.Repeat("x", artifactPreviewMaxBytes+1)
	if err := os.WriteFile(filepath.Join(project.Directory, "large.html"), []byte(large), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadArtifactContent(info.ID, "large.html"); err == nil || !strings.Contains(err.Error(), "2 MiB") {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestReadArtifactContentOfficeAndPDF(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Office artifacts")
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	realDirectory, err := filepath.EvalSymlinks(project.Directory)
	if err != nil {
		t.Fatal(err)
	}
	project.mu.Lock()
	project.Directory = realDirectory
	project.mu.Unlock()
	tool := tools.NewDocumentCreateTool(project.Directory)
	cases := []struct {
		name, format, spec, kind, previewText string
	}{
		{"report.docx", "docx", `{"title":"Отчёт","sections":[{"heading":"Итоги","paragraphs":["Рост подтверждён"]}]}`, "document", "Рост подтверждён"},
		{"model.xlsx", "xlsx", `{"title":"Модель","sheets":[{"name":"Итоги","rows":[["Метрика","Значение"],["Рост",18]]}]}`, "spreadsheet", "Метрика"},
		{"deck.pptx", "pptx", `{"title":"Стратегия","slides":[{"title":"Приоритеты","bullets":["Качество"]}]}`, "presentation", "Приоритеты"},
		{"brief.pdf", "pdf", `{"title":"Бриф","sections":[{"heading":"Решение","paragraphs":["Одобрено"]}]}`, "pdf", ""},
	}
	for _, test := range cases {
		t.Run(test.format, func(t *testing.T) {
			fullPath := filepath.Join(project.Directory, test.name)
			result, err := tool.Execute(context.Background(), map[string]any{
				"file_path": fullPath, "format": test.format, "content_json": test.spec,
			})
			if err != nil || !result.Success {
				t.Fatalf("create = %#v, %v", result, err)
			}
			artifact, err := s.ReadArtifactContent(info.ID, test.name)
			if err != nil {
				t.Fatal(err)
			}
			if artifact.PreviewKind != test.kind || artifact.DataBase64 == "" {
				t.Fatalf("artifact = %#v", artifact)
			}
			if _, err := base64.StdEncoding.DecodeString(artifact.DataBase64); err != nil {
				t.Fatalf("invalid download data: %v", err)
			}
			if test.previewText != "" && !strings.Contains(artifact.Content, test.previewText) {
				t.Fatalf("preview missing %q: %s", test.previewText, artifact.Content)
			}
		})
	}
}

func TestReadArtifactContentRejectsNonTextContent(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Artifact text validation")
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()

	cases := []struct {
		name string
		data []byte
	}{
		{name: "invalid-utf8.html", data: []byte{0xff, 0xfe, 0xfd}},
		{name: "nul.svg", data: []byte("<svg>\x00</svg>")},
	}
	for _, tc := range cases {
		if err := os.WriteFile(filepath.Join(project.Directory, tc.name), tc.data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadArtifactContent(info.ID, tc.name); err == nil ||
			!strings.Contains(err.Error(), "valid UTF-8 text") {
			t.Fatalf("%s error = %v", tc.name, err)
		}
	}
}

func TestSessionArtifactsAndVersionsStayInsideChatWorktrees(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	repo := prepareSessionWorktreeRepo(t)
	if err := writeFile(filepath.Join(repo, "dashboard.html"), "<h1>root</h1>"); err != nil {
		t.Fatal(err)
	}
	gitMust(t, repo, "add", "dashboard.html")
	gitMust(t, repo, "commit", "-m", "add artifact fixture")

	projectInfo, err := s.AddProject("session artifacts", repo)
	if err != nil {
		t.Fatal(err)
	}
	project := s.projects[projectInfo.ID]
	first := project.sessions["default"].Info()
	second, err := s.CreateChatSession(projectInfo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !first.WorktreeIsolated || !second.WorktreeIsolated {
		t.Fatalf("fixture sessions were not isolated: first=%+v second=%+v", first, second)
	}
	firstPath := filepath.Join(first.WorktreePath, "dashboard.html")
	secondPath := filepath.Join(second.WorktreePath, "dashboard.html")
	if err := writeFile(firstPath, "<h1>first-v1</h1>"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(secondPath, "<h1>second-v1</h1>"); err != nil {
		t.Fatal(err)
	}

	firstDocument, err := s.ReadSessionArtifactContent(projectInfo.ID, "default", "dashboard.html")
	if err != nil || firstDocument.Content != "<h1>first-v1</h1>" {
		t.Fatalf("first session document = %#v, %v", firstDocument, err)
	}
	secondDocument, err := s.ReadSessionArtifactContent(projectInfo.ID, second.ID, "dashboard.html")
	if err != nil || secondDocument.Content != "<h1>second-v1</h1>" {
		t.Fatalf("second session document = %#v, %v", secondDocument, err)
	}
	library, err := s.ListSessionArtifacts(projectInfo.ID, "default")
	if err != nil || len(library.Artifacts) != 1 || library.Artifacts[0].Path != "dashboard.html" {
		t.Fatalf("first session library = %#v, %v", library, err)
	}

	firstVersion, err := s.SaveSessionArtifactVersion(projectInfo.ID, "default", "dashboard.html")
	if err != nil {
		t.Fatal(err)
	}
	secondVersion, err := s.SaveSessionArtifactVersion(projectInfo.ID, second.ID, "dashboard.html")
	if err != nil {
		t.Fatal(err)
	}
	if firstVersion.Digest == secondVersion.Digest {
		t.Fatal("independent session histories unexpectedly stored identical content")
	}
	if versions, err := s.ListSessionArtifactVersions(projectInfo.ID, "default", "dashboard.html"); err != nil || len(versions) != 1 || versions[0].ID != firstVersion.ID {
		t.Fatalf("first session versions = %#v, %v", versions, err)
	}
	if versions, err := s.ListSessionArtifactVersions(projectInfo.ID, second.ID, "dashboard.html"); err != nil || len(versions) != 1 || versions[0].ID != secondVersion.ID {
		t.Fatalf("second session versions = %#v, %v", versions, err)
	}

	if err := writeFile(firstPath, "<h1>first-v2</h1>"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreSessionArtifactVersion(projectInfo.ID, "default", "dashboard.html", firstVersion.ID); err != nil {
		t.Fatal(err)
	}
	firstBytes, _ := os.ReadFile(firstPath)
	secondBytes, _ := os.ReadFile(secondPath)
	rootBytes, _ := os.ReadFile(filepath.Join(repo, "dashboard.html"))
	if string(firstBytes) != "<h1>first-v1</h1>" || string(secondBytes) != "<h1>second-v1</h1>" || string(rootBytes) != "<h1>root</h1>" {
		t.Fatalf("restore escaped session: first=%q second=%q root=%q", firstBytes, secondBytes, rootBytes)
	}
	if err := removeArtifactVersionsForSession(projectInfo.ID, "default"); err != nil {
		t.Fatal(err)
	}
	if versions, err := s.ListSessionArtifactVersions(projectInfo.ID, "default", "dashboard.html"); err != nil || len(versions) != 0 {
		t.Fatalf("deleted session versions remained = %#v, %v", versions, err)
	}
	if versions, err := s.ListSessionArtifactVersions(projectInfo.ID, second.ID, "dashboard.html"); err != nil || len(versions) != 1 || versions[0].ID != secondVersion.ID {
		t.Fatalf("session cleanup removed sibling history = %#v, %v", versions, err)
	}
}

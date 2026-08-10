package tools

import (
	"archive/zip"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentBuildersProduceValidNativePackages(t *testing.T) {
	narrative := DocumentSpec{
		Title:  "Квартальный отчёт",
		Author: "Gokin Test",
		Sections: []DocumentSection{{
			Heading: "Результаты", Level: 1,
			Paragraphs: []string{"Выручка выросла на 18%."},
			Bullets:    []string{"Проверено", "Согласовано"},
			Table: &DocumentTable{
				Headers: []string{"Метрика", "Значение"},
				Rows:    [][]string{{"Выручка", "₸42 млн"}},
			},
		}},
	}
	workbook := DocumentSpec{
		Title: "Финансовая модель",
		Sheets: []SpreadsheetSheet{{
			Name: "Итоги",
			Rows: [][]any{
				{"Статья", "Сумма", "С налогом"},
				{"Доход", json.Number("1200.5"), "=B2*1.12"},
				{"Расход", json.Number("400"), "=B3*1.12"},
			},
		}},
	}
	presentation := DocumentSpec{
		Title: "Стратегия",
		Slides: []PresentationSlide{
			{Title: "Стратегия 2027", Subtitle: "План роста"},
			{Title: "Приоритеты", Bullets: []string{"Качество", "Скорость", "Доверие"}},
		},
	}

	docx, err := buildDOCX(narrative)
	if err != nil {
		t.Fatal(err)
	}
	validateOOXMLPackage(t, docx, []string{"word/document.xml", "word/styles.xml"})
	if !zipPartContains(t, docx, "word/document.xml", "Квартальный отчёт") {
		t.Fatal("DOCX lost Unicode title")
	}

	xlsx, err := buildXLSX(workbook)
	if err != nil {
		t.Fatal(err)
	}
	validateOOXMLPackage(t, xlsx, []string{"xl/workbook.xml", "xl/styles.xml", "xl/worksheets/sheet1.xml"})
	if !zipPartContains(t, xlsx, "xl/worksheets/sheet1.xml", "<f>B2*1.12</f>") {
		t.Fatal("XLSX lost local formula")
	}

	pptx, err := buildPPTX(presentation)
	if err != nil {
		t.Fatal(err)
	}
	validateOOXMLPackage(t, pptx, []string{"ppt/presentation.xml", "ppt/slides/slide1.xml", "ppt/slides/slide2.xml"})
	if !zipPartContains(t, pptx, "ppt/slides/slide2.xml", "Приоритеты") {
		t.Fatal("PPTX lost Unicode slide title")
	}

	pdf, err := buildPDF(narrative)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(pdf), "%PDF-") || !strings.Contains(string(pdf[len(pdf)-64:]), "%%EOF") {
		t.Fatal("invalid PDF envelope")
	}
	if outputDir := os.Getenv("GOKIN_DOCUMENT_SAMPLE_DIR"); outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o750); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{
			"report.docx": docx, "model.xlsx": xlsx,
			"strategy.pptx": pptx, "report.pdf": pdf,
		} {
			if err := os.WriteFile(filepath.Join(outputDir, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestDocumentCreateToolWritesAtomicallyAndRequiresReplace(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := NewDocumentCreateTool(root)
	target := filepath.Join(root, "reports", "brief.docx")
	spec := `{"title":"Brief","sections":[{"heading":"Summary","paragraphs":["Reviewed evidence."]}]}`
	args := map[string]any{"file_path": target, "format": "docx", "content_json": spec}
	result, err := tool.Execute(context.Background(), args)
	if err != nil || !result.Success {
		t.Fatalf("first create = %#v, %v", result, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Size() == 0 {
		t.Fatalf("created file = %v, %v", info, err)
	}
	result, err = tool.Execute(context.Background(), args)
	if err != nil || result.Success || !strings.Contains(result.Error, "already exists") {
		t.Fatalf("unexpected overwrite result = %#v, %v", result, err)
	}
	args["replace"] = true
	result, err = tool.Execute(context.Background(), args)
	if err != nil || !result.Success {
		t.Fatalf("replace = %#v, %v", result, err)
	}
}

func TestReadToolExtractsGeneratedOfficeDocuments(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	specs := []struct {
		name string
		data func() ([]byte, error)
		want string
	}{
		{"report.docx", func() ([]byte, error) {
			return buildDOCX(DocumentSpec{Title: "Отчёт", Sections: []DocumentSection{{Paragraphs: []string{"Проверено"}}}})
		}, "Проверено"},
		{"model.xlsx", func() ([]byte, error) {
			return buildXLSX(DocumentSpec{Title: "Модель", Sheets: []SpreadsheetSheet{{Name: "Итоги", Rows: [][]any{{"Сумма"}, {"=SUM(A1:A1)"}}}}})
		}, "=SUM(A1:A1)"},
		{"deck.pptx", func() ([]byte, error) {
			return buildPPTX(DocumentSpec{Title: "Deck", Slides: []PresentationSlide{{Title: "Приоритеты", Bullets: []string{"Качество"}}}})
		}, "Качество"},
	}
	reader := NewReadTool(root)
	for _, test := range specs {
		t.Run(test.name, func(t *testing.T) {
			data, err := test.data()
			if err != nil {
				t.Fatal(err)
			}
			filePath := filepath.Join(root, test.name)
			if err := os.WriteFile(filePath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := reader.Execute(context.Background(), map[string]any{"file_path": filePath})
			if err != nil || !result.Success || !strings.Contains(result.Content, test.want) {
				t.Fatalf("read = %#v, %v", result, err)
			}
		})
	}
}

func TestDocumentCreateRejectsUnsafePathAndFormula(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := NewDocumentCreateTool(root)
	outside := filepath.Join(filepath.Dir(root), "escape.xlsx")
	unsafeFormula := `{"title":"Unsafe","sheets":[{"name":"Sheet","rows":[["x"],["=HYPERLINK(\"https://evil.example\",\"click\")"]]}]}`
	for name, args := range map[string]map[string]any{
		"path": {
			"file_path": outside, "format": "xlsx",
			"content_json": `{"title":"Safe","sheets":[{"name":"Sheet","rows":[["x"]]}]}`,
		},
		"formula": {
			"file_path": filepath.Join(root, "unsafe.xlsx"), "format": "xlsx",
			"content_json": unsafeFormula,
		},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), args)
			if err != nil || result.Success {
				t.Fatalf("unsafe document accepted: %#v, %v", result, err)
			}
		})
	}
}

func TestDocumentSpecLimitsAndStrictJSON(t *testing.T) {
	cases := []struct {
		name, format, raw string
	}{
		{"unknown field", "docx", `{"title":"x","unknown":true,"sections":[{"paragraphs":["x"]}]}`},
		{"empty sections", "docx", `{"title":"x","sections":[]}`},
		{"duplicate sheets", "xlsx", `{"title":"x","sheets":[{"name":"Data","rows":[["x"]]},{"name":"data","rows":[["y"]]}]}`},
		{"empty slides", "pptx", `{"title":"x","slides":[]}`},
		{"trailing JSON", "pdf", `{"title":"x","sections":[{"paragraphs":["x"]}]} {}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseAndValidateDocumentSpec(test.format, test.raw); err == nil {
				t.Fatal("invalid spec accepted")
			}
		})
	}
}

func validateOOXMLPackage(t *testing.T, data []byte, required []string) {
	t.Helper()
	reader, err := zip.NewReader(strings.NewReader(string(data)), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	parts := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(stream)
		_ = stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		parts[file.Name] = content
		if strings.HasSuffix(file.Name, ".xml") || strings.HasSuffix(file.Name, ".rels") {
			decoder := xml.NewDecoder(strings.NewReader(string(content)))
			for {
				if _, err := decoder.Token(); err == io.EOF {
					break
				} else if err != nil {
					t.Fatalf("invalid XML %s: %v", file.Name, err)
				}
			}
		}
	}
	for _, name := range append([]string{"[Content_Types].xml", "_rels/.rels", "docProps/core.xml"}, required...) {
		if _, ok := parts[name]; !ok {
			t.Fatalf("package missing %s", name)
		}
	}
	for name, content := range parts {
		if !strings.HasSuffix(name, ".rels") {
			continue
		}
		var relationships struct {
			Items []struct {
				Target     string `xml:"Target,attr"`
				TargetMode string `xml:"TargetMode,attr"`
			} `xml:"Relationship"`
		}
		if err := xml.Unmarshal(content, &relationships); err != nil {
			t.Fatal(err)
		}
		base := relationshipSourceDir(name)
		for _, relationship := range relationships.Items {
			if strings.EqualFold(relationship.TargetMode, "External") {
				t.Fatalf("external relationship in generated package: %s -> %s", name, relationship.Target)
			}
			target := path.Clean(path.Join(base, relationship.Target))
			if strings.HasPrefix(target, "../") {
				t.Fatalf("relationship escapes package: %s -> %s", name, relationship.Target)
			}
			if _, ok := parts[target]; !ok {
				t.Fatalf("broken relationship: %s -> %s (%s)", name, relationship.Target, target)
			}
		}
	}
}

func relationshipSourceDir(rels string) string {
	if rels == "_rels/.rels" {
		return ""
	}
	dir := path.Dir(rels)
	if path.Base(dir) == "_rels" {
		dir = path.Dir(dir)
	}
	return dir
}

func zipPartContains(t *testing.T, data []byte, name, needle string) bool {
	t.Helper()
	reader, err := zip.NewReader(strings.NewReader(string(data)), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(stream)
		_ = stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(content), needle)
	}
	return false
}

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/security"
	"google.golang.org/genai"
)

const (
	maxDocumentSpecBytes    = 1 << 20
	maxGeneratedDocument    = 30 << 20
	maxDocumentSections     = 500
	maxDocumentParagraphs   = 4000
	maxDocumentTextRunes    = 500_000
	maxSpreadsheetSheets    = 20
	maxSpreadsheetRows      = 5000
	maxSpreadsheetColumns   = 100
	maxSpreadsheetCells     = 50_000
	maxPresentationSlides   = 100
	maxPresentationBullets  = 50
	maxDocumentTableColumns = 30
	maxDocumentTableRows    = 2000
)

// DocumentSpec is the format-neutral, bounded content model accepted by the
// document_create tool. Only fields relevant to the selected format are used.
type DocumentSpec struct {
	Title    string              `json:"title"`
	Subtitle string              `json:"subtitle,omitempty"`
	Author   string              `json:"author,omitempty"`
	Sections []DocumentSection   `json:"sections,omitempty"`
	Sheets   []SpreadsheetSheet  `json:"sheets,omitempty"`
	Slides   []PresentationSlide `json:"slides,omitempty"`
}

type DocumentSection struct {
	Heading    string         `json:"heading,omitempty"`
	Level      int            `json:"level,omitempty"`
	Paragraphs []string       `json:"paragraphs,omitempty"`
	Bullets    []string       `json:"bullets,omitempty"`
	Table      *DocumentTable `json:"table,omitempty"`
}

type DocumentTable struct {
	Headers []string   `json:"headers,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
}

type SpreadsheetSheet struct {
	Name string  `json:"name"`
	Rows [][]any `json:"rows"`
}

type PresentationSlide struct {
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle,omitempty"`
	Bullets  []string `json:"bullets,omitempty"`
	Footer   string   `json:"footer,omitempty"`
}

type DocumentCreateTool struct {
	workDir       string
	pathValidator *security.PathValidator
}

func NewDocumentCreateTool(workDir string) *DocumentCreateTool {
	return &DocumentCreateTool{
		workDir:       workDir,
		pathValidator: security.NewPathValidator([]string{workDir}, false),
	}
}

func (t *DocumentCreateTool) Name() string { return "document_create" }

func (t *DocumentCreateTool) Description() string {
	return "Creates a professional DOCX, XLSX, PPTX, or Unicode PDF in the project from a bounded JSON specification. Use sections for DOCX/PDF, sheets for XLSX, and slides for PPTX. Files are native, editable Office documents; no macros, remote content, or external services are used."
}

func (t *DocumentCreateTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"file_path": {
					Type:        genai.TypeString,
					Description: "Absolute output path beneath the project. Extension must match format.",
				},
				"format": {
					Type:        genai.TypeString,
					Enum:        []string{"docx", "xlsx", "pptx", "pdf"},
					Description: "Native output format.",
				},
				"content_json": {
					Type:        genai.TypeString,
					Description: `JSON object. Common fields: title, subtitle, author. DOCX/PDF: sections=[{heading,level,paragraphs:[...],bullets:[...],table:{headers:[...],rows:[[...]]}}]. XLSX: sheets=[{name,rows:[[value,...]]}]; values may be strings/numbers/booleans, and safe local formulas start with "=". PPTX: slides=[{title,subtitle,bullets:[...],footer}].`,
				},
				"replace": {
					Type:        genai.TypeBoolean,
					Description: "Replace an existing file. Default false. Replacement receives a fresh exact-action approval.",
				},
			},
			Required: []string{"file_path", "format", "content_json"},
		},
	}
}

func (t *DocumentCreateTool) Validate(args map[string]any) error {
	filePath, ok := GetString(args, "file_path")
	if !ok || strings.TrimSpace(filePath) == "" {
		return NewValidationError("file_path", "is required")
	}
	format, ok := GetString(args, "format")
	if !ok || !isDocumentFormat(format) {
		return NewValidationError("format", "must be docx, xlsx, pptx, or pdf")
	}
	content, ok := GetString(args, "content_json")
	if !ok || strings.TrimSpace(content) == "" {
		return NewValidationError("content_json", "is required")
	}
	if len(content) > maxDocumentSpecBytes {
		return NewValidationError("content_json", "exceeds the 1 MiB limit")
	}
	if !utf8.ValidString(content) {
		return NewValidationError("content_json", "must be valid UTF-8")
	}
	if strings.ToLower(filepath.Ext(filePath)) != "."+strings.ToLower(format) {
		return NewValidationError("file_path", "extension must match format")
	}
	_, err := parseAndValidateDocumentSpec(format, content)
	return err
}

func (t *DocumentCreateTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	if err := ctx.Err(); err != nil {
		return NewErrorResult("document creation cancelled"), nil
	}
	filePath, _ := GetString(args, "file_path")
	format, _ := GetString(args, "format")
	content, _ := GetString(args, "content_json")
	replace := GetBoolDefault(args, "replace", false)

	if t.pathValidator == nil {
		return NewErrorResult("security error: path validator not initialized"), nil
	}
	validPath, err := t.pathValidator.Validate(filePath)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("path validation failed: %s", err)), nil
	}
	if err := security.IsBlockedWritePath(validPath); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	info, statErr := os.Lstat(validPath)
	switch {
	case statErr == nil && !replace:
		return NewErrorResult("output file already exists; inspect it first, then retry with replace=true"), nil
	case statErr == nil && !info.Mode().IsRegular():
		return NewErrorResult("output path exists and is not a regular file"), nil
	case statErr != nil && !os.IsNotExist(statErr):
		return NewErrorResult(fmt.Sprintf("stat output file: %s", statErr)), nil
	}
	spec, err := parseAndValidateDocumentSpec(format, content)
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}

	var data []byte
	switch strings.ToLower(format) {
	case "docx":
		data, err = buildDOCX(spec)
	case "xlsx":
		data, err = buildXLSX(spec)
	case "pptx":
		data, err = buildPPTX(spec)
	case "pdf":
		data, err = buildPDF(spec)
	}
	if err != nil {
		return NewErrorResult(fmt.Sprintf("generate %s: %s", format, err)), nil
	}
	if len(data) == 0 || len(data) > maxGeneratedDocument {
		return NewErrorResult(fmt.Sprintf("generated document must be 1 byte to %d MiB", maxGeneratedDocument>>20)), nil
	}
	if err := ctx.Err(); err != nil {
		return NewErrorResult("document creation cancelled before write"), nil
	}
	if err := os.MkdirAll(filepath.Dir(validPath), 0o750); err != nil {
		return NewErrorResult(fmt.Sprintf("create output directory: %s", err)), nil
	}
	if err := AtomicWrite(validPath, data, 0o644); err != nil {
		return NewErrorResult(fmt.Sprintf("write document: %s", err)), nil
	}
	EmitFilePeek(ctx, validPath, "Created document", fmt.Sprintf("%s · %d bytes · %s", strings.ToUpper(format), len(data), spec.Title), t.Name())
	status := "Created"
	if statErr == nil {
		status = "Replaced"
	}
	return NewSuccessResult(fmt.Sprintf(
		"%s %s: %s (%d bytes). The native file is ready to open, download, or refine.",
		status, strings.ToUpper(format), validPath, len(data),
	)), nil
}

func isDocumentFormat(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "docx", "xlsx", "pptx", "pdf":
		return true
	default:
		return false
	}
}

func parseAndValidateDocumentSpec(format, raw string) (DocumentSpec, error) {
	var spec DocumentSpec
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return spec, NewValidationError("content_json", "invalid document JSON: "+err.Error())
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return spec, NewValidationError("content_json", err.Error())
	}
	spec.Title = strings.TrimSpace(spec.Title)
	spec.Subtitle = strings.TrimSpace(spec.Subtitle)
	spec.Author = strings.TrimSpace(spec.Author)
	if spec.Title == "" {
		return spec, NewValidationError("content_json.title", "is required")
	}
	if utf8.RuneCountInString(spec.Title) > 300 || utf8.RuneCountInString(spec.Subtitle) > 1000 ||
		utf8.RuneCountInString(spec.Author) > 200 {
		return spec, NewValidationError("content_json", "title, subtitle, or author is too long")
	}
	switch strings.ToLower(format) {
	case "docx", "pdf":
		if err := validateNarrativeSpec(&spec); err != nil {
			return spec, err
		}
	case "xlsx":
		if err := validateSpreadsheetSpec(&spec); err != nil {
			return spec, err
		}
	case "pptx":
		if err := validatePresentationSpec(&spec); err != nil {
			return spec, err
		}
	}
	return spec, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("must contain exactly one JSON object")
	}
	return fmt.Errorf("invalid trailing JSON: %v", err)
}

func validateNarrativeSpec(spec *DocumentSpec) error {
	if len(spec.Sections) == 0 || len(spec.Sections) > maxDocumentSections {
		return NewValidationError("content_json.sections", fmt.Sprintf("must contain 1-%d sections", maxDocumentSections))
	}
	totalParagraphs, totalRunes := 0, utf8.RuneCountInString(spec.Title)+utf8.RuneCountInString(spec.Subtitle)
	for i := range spec.Sections {
		section := &spec.Sections[i]
		if section.Level == 0 {
			section.Level = 1
		}
		if section.Level < 1 || section.Level > 3 {
			return NewValidationError(fmt.Sprintf("content_json.sections[%d].level", i), "must be 1-3")
		}
		if len(section.Paragraphs)+len(section.Bullets) == 0 && section.Table == nil && strings.TrimSpace(section.Heading) == "" {
			return NewValidationError(fmt.Sprintf("content_json.sections[%d]", i), "is empty")
		}
		totalParagraphs += len(section.Paragraphs) + len(section.Bullets)
		totalRunes += utf8.RuneCountInString(section.Heading)
		for _, text := range append(append([]string(nil), section.Paragraphs...), section.Bullets...) {
			if !validDocumentText(text) {
				return NewValidationError(fmt.Sprintf("content_json.sections[%d]", i), "contains invalid or oversized text")
			}
			totalRunes += utf8.RuneCountInString(text)
		}
		if section.Table != nil {
			if err := validateDocumentTable(section.Table, i); err != nil {
				return err
			}
			totalParagraphs += len(section.Table.Rows) + 1
			for _, value := range section.Table.Headers {
				totalRunes += utf8.RuneCountInString(value)
			}
			for _, row := range section.Table.Rows {
				for _, value := range row {
					totalRunes += utf8.RuneCountInString(value)
				}
			}
		}
	}
	if totalParagraphs > maxDocumentParagraphs || totalRunes > maxDocumentTextRunes {
		return NewValidationError("content_json.sections", "document content exceeds the bounded text limit")
	}
	return nil
}

func validateDocumentTable(table *DocumentTable, section int) error {
	columns := len(table.Headers)
	if columns == 0 || columns > maxDocumentTableColumns || len(table.Rows) > maxDocumentTableRows {
		return NewValidationError(fmt.Sprintf("content_json.sections[%d].table", section), "invalid table dimensions")
	}
	for _, header := range table.Headers {
		if !validDocumentText(header) {
			return NewValidationError(fmt.Sprintf("content_json.sections[%d].table.headers", section), "contains invalid text")
		}
	}
	for rowIndex, row := range table.Rows {
		if len(row) != columns {
			return NewValidationError(fmt.Sprintf("content_json.sections[%d].table.rows[%d]", section, rowIndex), "column count must match headers")
		}
		for _, value := range row {
			if !validDocumentText(value) {
				return NewValidationError(fmt.Sprintf("content_json.sections[%d].table.rows[%d]", section, rowIndex), "contains invalid text")
			}
		}
	}
	return nil
}

func validateSpreadsheetSpec(spec *DocumentSpec) error {
	if len(spec.Sheets) == 0 || len(spec.Sheets) > maxSpreadsheetSheets {
		return NewValidationError("content_json.sheets", fmt.Sprintf("must contain 1-%d sheets", maxSpreadsheetSheets))
	}
	seen, cells := map[string]bool{}, 0
	for sheetIndex := range spec.Sheets {
		sheet := &spec.Sheets[sheetIndex]
		sheet.Name = sanitizeSheetName(sheet.Name, sheetIndex+1)
		key := strings.ToLower(sheet.Name)
		if seen[key] {
			return NewValidationError(fmt.Sprintf("content_json.sheets[%d].name", sheetIndex), "must be unique")
		}
		seen[key] = true
		if len(sheet.Rows) == 0 || len(sheet.Rows) > maxSpreadsheetRows {
			return NewValidationError(fmt.Sprintf("content_json.sheets[%d].rows", sheetIndex), "invalid row count")
		}
		for rowIndex, row := range sheet.Rows {
			if len(row) > maxSpreadsheetColumns {
				return NewValidationError(fmt.Sprintf("content_json.sheets[%d].rows[%d]", sheetIndex, rowIndex), "too many columns")
			}
			cells += len(row)
			for columnIndex, value := range row {
				if err := validateSpreadsheetValue(value); err != nil {
					return NewValidationError(
						fmt.Sprintf("content_json.sheets[%d].rows[%d][%d]", sheetIndex, rowIndex, columnIndex),
						err.Error(),
					)
				}
			}
		}
	}
	if cells > maxSpreadsheetCells {
		return NewValidationError("content_json.sheets", fmt.Sprintf("exceeds %d cells", maxSpreadsheetCells))
	}
	return nil
}

func validateSpreadsheetValue(value any) error {
	switch typed := value.(type) {
	case nil, bool, json.Number:
		return nil
	case string:
		if !validDocumentText(typed) {
			return fmt.Errorf("contains invalid or oversized text")
		}
		if strings.HasPrefix(typed, "=") && !safeSpreadsheetFormula(typed) {
			return fmt.Errorf("contains a blocked external or executable formula")
		}
		return nil
	default:
		return fmt.Errorf("must be string, number, boolean, or null")
	}
}

func validatePresentationSpec(spec *DocumentSpec) error {
	if len(spec.Slides) == 0 || len(spec.Slides) > maxPresentationSlides {
		return NewValidationError("content_json.slides", fmt.Sprintf("must contain 1-%d slides", maxPresentationSlides))
	}
	totalRunes := utf8.RuneCountInString(spec.Title) + utf8.RuneCountInString(spec.Subtitle)
	for i := range spec.Slides {
		slide := &spec.Slides[i]
		slide.Title = strings.TrimSpace(slide.Title)
		if slide.Title == "" || len(slide.Bullets) > maxPresentationBullets {
			return NewValidationError(fmt.Sprintf("content_json.slides[%d]", i), "requires a title and at most 50 bullets")
		}
		for _, value := range append([]string{slide.Title, slide.Subtitle, slide.Footer}, slide.Bullets...) {
			if !validDocumentText(value) {
				return NewValidationError(fmt.Sprintf("content_json.slides[%d]", i), "contains invalid or oversized text")
			}
			totalRunes += utf8.RuneCountInString(value)
		}
	}
	if totalRunes > maxDocumentTextRunes {
		return NewValidationError("content_json.slides", "presentation text exceeds the bounded limit")
	}
	return nil
}

func validDocumentText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= 20_000
}

func sanitizeSheetName(value string, index int) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\\`, r) || r < 0x20 {
			return -1
		}
		return r
	}, value)
	if value == "" {
		value = fmt.Sprintf("Sheet %d", index)
	}
	runes := []rune(value)
	if len(runes) > 31 {
		value = string(runes[:31])
	}
	return value
}

func safeSpreadsheetFormula(formula string) bool {
	if len(formula) < 2 || len(formula) > 2048 || strings.ContainsAny(formula, "\x00\r\n") {
		return false
	}
	lower := strings.ToLower(formula)
	for _, marker := range []string{
		"http:", "https:", "ftp:", "file:", "cmd", "powershell", "dde",
		"webservice(", "hyperlink(", "rtd(", "call(", "exec(", "register.id(",
		"[", "]", "|",
	} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

func buildZipPackage(files map[string][]byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := newDeterministicZipWriter(&buffer)
	for _, name := range sortedMapKeys(files) {
		if err := writer.add(name, files[name]); err != nil {
			_ = writer.close()
			return nil, err
		}
	}
	if err := writer.close(); err != nil {
		return nil, err
	}
	if buffer.Len() > maxGeneratedDocument {
		return nil, fmt.Errorf("generated package exceeds %d MiB", maxGeneratedDocument>>20)
	}
	return buffer.Bytes(), nil
}

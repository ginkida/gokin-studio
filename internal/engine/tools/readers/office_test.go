package readers

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOfficeReaderBoundsRepeatedSharedStrings(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	parts := map[string]string{
		"xl/workbook.xml": `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheets><sheet name="Data"/></sheets></workbook>`,
		"xl/sharedStrings.xml": `<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>` +
			strings.Repeat("я", officeReaderMaxCellBytes) + `</t></si></sst>`,
	}
	var worksheet strings.Builder
	worksheet.WriteString(`<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for row := 1; row <= 1000; row++ {
		worksheet.WriteString(fmt.Sprintf(`<row r="%d"><c r="A%d" t="s"><v>0</v></c></row>`, row, row))
	}
	worksheet.WriteString(`</sheetData></worksheet>`)
	parts["xl/worksheets/sheet1.xml"] = worksheet.String()
	for name, content := range parts {
		part, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(t.TempDir(), "bounded.xlsx")
	if err := os.WriteFile(filePath, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := NewOfficeReader().Read(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) > officeReaderMaxOutputBytes {
		t.Fatalf("extracted %d bytes, max %d", len(content), officeReaderMaxOutputBytes)
	}
	if !utf8.ValidString(content) {
		t.Fatal("bounded extraction split a UTF-8 sequence")
	}
}

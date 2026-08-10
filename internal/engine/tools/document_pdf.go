package tools

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/phpdave11/gofpdf"
)

func buildPDF(spec DocumentSpec) ([]byte, error) {
	fontPath, err := findUnicodeDocumentFont()
	if err != nil {
		return nil, err
	}
	fontData, err := os.ReadFile(fontPath)
	if err != nil {
		return nil, fmt.Errorf("read Unicode font: %w", err)
	}
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(20, 18, 20)
	pdf.SetAutoPageBreak(true, 18)
	pdf.SetTitle(spec.Title, true)
	author := spec.Author
	if author == "" {
		author = "Gokin Studio"
	}
	pdf.SetAuthor(author, true)
	pdf.SetCreator("Gokin Studio", true)
	pdf.AddUTF8FontFromBytes("GokinUnicode", "", fontData)
	if pdf.Error() != nil {
		return nil, pdf.Error()
	}
	pdf.AliasNbPages("")
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("GokinUnicode", "", 8)
		pdf.SetTextColor(110, 123, 134)
		pdf.CellFormat(0, 6, fmt.Sprintf("%s  ·  %d/{nb}", spec.Title, pdf.PageNo()), "", 0, "C", false, 0, "")
	})

	pdf.AddPage()
	pdf.SetFont("GokinUnicode", "", 26)
	pdf.SetTextColor(23, 54, 93)
	pdf.MultiCell(0, 11, spec.Title, "", "L", false)
	pdf.SetDrawColor(46, 117, 182)
	pdf.SetLineWidth(1.2)
	pdf.Line(20, pdf.GetY()+2, 70, pdf.GetY()+2)
	pdf.Ln(8)
	if spec.Subtitle != "" {
		pdf.SetFont("GokinUnicode", "", 13)
		pdf.SetTextColor(91, 101, 115)
		pdf.MultiCell(0, 7, spec.Subtitle, "", "L", false)
		pdf.Ln(3)
	}
	if spec.Author != "" {
		pdf.SetFont("GokinUnicode", "", 9)
		pdf.SetTextColor(110, 123, 134)
		pdf.CellFormat(0, 6, spec.Author, "", 1, "L", false, 0, "")
		pdf.Ln(3)
	}

	for _, section := range spec.Sections {
		if section.Heading != "" {
			size, spacing, color := 18.0, 8.0, [3]int{31, 78, 120}
			if section.Level == 2 {
				size, spacing, color = 14, 7, [3]int{46, 117, 182}
			} else if section.Level == 3 {
				size, spacing, color = 11, 6, [3]int{54, 95, 145}
			}
			pdf.Ln(2)
			pdf.SetFont("GokinUnicode", "", size)
			pdf.SetTextColor(color[0], color[1], color[2])
			pdf.MultiCell(0, spacing, section.Heading, "", "L", false)
			pdf.Ln(1)
		}
		for _, paragraph := range section.Paragraphs {
			pdf.SetFont("GokinUnicode", "", 10.5)
			pdf.SetTextColor(38, 50, 56)
			pdf.MultiCell(0, 5.8, paragraph, "", "J", false)
			pdf.Ln(2)
		}
		for _, bullet := range section.Bullets {
			pdf.SetFont("GokinUnicode", "", 10.5)
			pdf.SetTextColor(38, 50, 56)
			x := pdf.GetX()
			pdf.CellFormat(7, 5.8, "•", "", 0, "C", false, 0, "")
			pdf.MultiCell(0, 5.8, bullet, "", "L", false)
			pdf.SetX(x)
			pdf.Ln(1)
		}
		if section.Table != nil {
			drawPDFTable(pdf, *section.Table)
			pdf.Ln(3)
		}
	}

	var buffer bytes.Buffer
	if err := pdf.Output(&buffer); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func drawPDFTable(pdf *gofpdf.Fpdf, table DocumentTable) {
	columns := len(table.Headers)
	if columns == 0 {
		return
	}
	available := 170.0
	width := available / float64(columns)
	if pdf.GetY() > 260 {
		pdf.AddPage()
	}
	pdf.SetFont("GokinUnicode", "", 9)
	pdf.SetFillColor(31, 78, 120)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetDrawColor(180, 198, 231)
	for _, header := range table.Headers {
		pdf.CellFormat(width, 7, truncatePDFCell(header), "1", 0, "L", true, 0, "")
	}
	pdf.Ln(-1)
	for rowIndex, row := range table.Rows {
		if pdf.GetY() > 270 {
			pdf.AddPage()
		}
		if rowIndex%2 == 1 {
			pdf.SetFillColor(234, 242, 248)
		} else {
			pdf.SetFillColor(255, 255, 255)
		}
		pdf.SetTextColor(38, 50, 56)
		for _, value := range row {
			pdf.CellFormat(width, 7, truncatePDFCell(value), "1", 0, "L", true, 0, "")
		}
		pdf.Ln(-1)
	}
}

func truncatePDFCell(value string) string {
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > 60 {
		return string(runes[:59]) + "…"
	}
	return string(runes)
}

func findUnicodeDocumentFont() (string, error) {
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/System/Library/Fonts/Supplemental/Arial.ttf",
			"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
			"/Library/Fonts/Arial.ttf",
		}
	case "windows":
		windowsDir := os.Getenv("WINDIR")
		if windowsDir == "" {
			windowsDir = `C:\Windows`
		}
		candidates = []string{
			filepath.Join(windowsDir, "Fonts", "arial.ttf"),
			filepath.Join(windowsDir, "Fonts", "segoeui.ttf"),
			filepath.Join(windowsDir, "Fonts", "calibri.ttf"),
		}
	default:
		candidates = []string{
			"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
			"/usr/share/fonts/dejavu/DejaVuSans.ttf",
			"/usr/share/fonts/truetype/liberation2/LiberationSans-Regular.ttf",
			"/usr/share/fonts/opentype/noto/NotoSans-Regular.ttf",
			"/usr/local/share/fonts/DejaVuSans.ttf",
		}
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= 32<<20 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no supported Unicode TrueType font found; install Arial, DejaVu Sans, Liberation Sans, or Noto Sans")
}

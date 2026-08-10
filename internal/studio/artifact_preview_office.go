package studio

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	officePreviewMaxParts     = 2048
	officePreviewMaxText      = 1 << 20
	officePreviewBodyBudget   = 3 << 20
	officePreviewTextMaxBytes = 32 << 10
)

func buildOfficeArtifactPreview(kind string, data []byte) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open OOXML package: %w", err)
	}
	if len(reader.File) == 0 || len(reader.File) > officePreviewMaxParts {
		return "", fmt.Errorf("OOXML package must contain 1-%d parts", officePreviewMaxParts)
	}
	switch kind {
	case "document":
		return previewDOCX(reader.File)
	case "spreadsheet":
		return previewXLSX(reader.File)
	case "presentation":
		return previewPPTX(reader.File)
	default:
		return "", fmt.Errorf("unsupported Office preview kind")
	}
}

func previewDOCX(files []*zip.File) (string, error) {
	document := findZipFile(files, "word/document.xml")
	if document == nil {
		return "", fmt.Errorf("DOCX is missing word/document.xml")
	}
	paragraphs, err := officeTextParagraphs(document, officePreviewMaxText)
	if err != nil {
		return "", err
	}
	if len(paragraphs) == 0 {
		return "", fmt.Errorf("DOCX contains no previewable text")
	}
	var body strings.Builder
	body.WriteString(`<main class="document-paper">`)
	for index, paragraph := range paragraphs {
		if body.Len() >= officePreviewBodyBudget {
			body.WriteString(`<p>Preview truncated. Use the read tool for bounded text extraction.</p>`)
			break
		}
		tag := "p"
		if index == 0 {
			tag = "h1"
		} else if len([]rune(paragraph)) < 90 && !strings.HasSuffix(paragraph, ".") && !strings.HasSuffix(paragraph, "。") {
			tag = "h2"
		}
		body.WriteString("<" + tag + ">" + html.EscapeString(officePreviewTextPrefix(paragraph)) + "</" + tag + ">")
	}
	body.WriteString(`</main>`)
	return officePreviewHTML("Document preview", officeDocumentCSS(), body.String()), nil
}

func previewPPTX(files []*zip.File) (string, error) {
	slides := make([]*zip.File, 0)
	for _, file := range files {
		if strings.HasPrefix(file.Name, "ppt/slides/slide") &&
			strings.HasSuffix(file.Name, ".xml") &&
			!strings.Contains(file.Name, "/_rels/") {
			slides = append(slides, file)
		}
	}
	sort.Slice(slides, func(i, j int) bool { return naturalPartLess(slides[i].Name, slides[j].Name) })
	if len(slides) == 0 {
		return "", fmt.Errorf("PPTX contains no slides")
	}
	var body strings.Builder
	body.WriteString(`<main class="deck">`)
	for index, slide := range slides {
		if body.Len() >= officePreviewBodyBudget {
			body.WriteString(`<p>Preview truncated. Use the read tool for bounded text extraction.</p>`)
			break
		}
		paragraphs, err := officeTextParagraphs(slide, officePreviewMaxText)
		if err != nil {
			return "", err
		}
		body.WriteString(`<section class="slide"><span class="slide-number">` + strconv.Itoa(index+1) + `</span>`)
		if len(paragraphs) > 0 {
			body.WriteString(`<h1>` + html.EscapeString(officePreviewTextPrefix(paragraphs[0])) + `</h1>`)
		}
		if len(paragraphs) > 1 {
			body.WriteString(`<ul>`)
			for _, paragraph := range paragraphs[1:] {
				if body.Len() >= officePreviewBodyBudget {
					body.WriteString(`<li>Preview truncated. Use the read tool for bounded text extraction.</li>`)
					break
				}
				body.WriteString(`<li>` + html.EscapeString(officePreviewTextPrefix(paragraph)) + `</li>`)
			}
			body.WriteString(`</ul>`)
		}
		body.WriteString(`</section>`)
	}
	body.WriteString(`</main>`)
	return officePreviewHTML("Presentation preview", officePresentationCSS(), body.String()), nil
}

func previewXLSX(files []*zip.File) (string, error) {
	shared, err := xlsxSharedStrings(files)
	if err != nil {
		return "", err
	}
	names, err := xlsxSheetNames(files)
	if err != nil {
		return "", err
	}
	sheets := make([]*zip.File, 0)
	for _, file := range files {
		if strings.HasPrefix(file.Name, "xl/worksheets/sheet") && strings.HasSuffix(file.Name, ".xml") {
			sheets = append(sheets, file)
		}
	}
	sort.Slice(sheets, func(i, j int) bool { return naturalPartLess(sheets[i].Name, sheets[j].Name) })
	if len(sheets) == 0 {
		return "", fmt.Errorf("XLSX contains no worksheets")
	}
	if len(sheets) > 3 {
		sheets = sheets[:3]
	}
	var body strings.Builder
	body.WriteString(`<main class="workbook">`)
	for index, sheet := range sheets {
		rows, err := xlsxRows(sheet, shared)
		if err != nil {
			return "", err
		}
		name := fmt.Sprintf("Sheet %d", index+1)
		if index < len(names) && names[index] != "" {
			name = names[index]
		}
		body.WriteString(`<section class="sheet"><h2>` + html.EscapeString(officePreviewTextPrefix(name)) + `</h2><div class="table-scroll"><table>`)
		truncated := false
		for rowIndex, row := range rows {
			body.WriteString(`<tr>`)
			for _, value := range row {
				if body.Len() >= officePreviewBodyBudget {
					truncated = true
					break
				}
				tag := "td"
				if rowIndex == 0 {
					tag = "th"
				}
				body.WriteString("<" + tag + ">" + html.EscapeString(officePreviewTextPrefix(value)) + "</" + tag + ">")
			}
			body.WriteString(`</tr>`)
			if truncated {
				body.WriteString(`<tr><td>Preview truncated. Use the read tool for bounded text extraction.</td></tr>`)
				break
			}
		}
		body.WriteString(`</table></div></section>`)
	}
	body.WriteString(`</main>`)
	return officePreviewHTML("Spreadsheet preview", officeSpreadsheetCSS(), body.String()), nil
}

func officePreviewTextPrefix(value string) string {
	if len(value) <= officePreviewTextMaxBytes {
		return value
	}
	end := officePreviewTextMaxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "…"
}

func officeTextParagraphs(file *zip.File, maxBytes int64) ([]string, error) {
	stream, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	limited := &io.LimitedReader{R: stream, N: maxBytes + 1}
	decoder := xml.NewDecoder(limited)
	var paragraphs []string
	var current strings.Builder
	inText := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", file.Name, err)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "t":
				inText = true
			case "tab":
				current.WriteByte('\t')
			case "br", "cr":
				current.WriteByte('\n')
			}
		case xml.EndElement:
			switch typed.Name.Local {
			case "t":
				inText = false
			case "p":
				text := normalizeOfficePreviewText(current.String())
				if text != "" {
					paragraphs = append(paragraphs, text)
				}
				current.Reset()
			}
		case xml.CharData:
			if inText {
				current.Write([]byte(typed))
			}
		}
		if len(paragraphs) >= 1000 {
			break
		}
	}
	if limited.N == 0 {
		return nil, fmt.Errorf("%s exceeds the preview extraction limit", file.Name)
	}
	if text := normalizeOfficePreviewText(current.String()); text != "" {
		paragraphs = append(paragraphs, text)
	}
	return paragraphs, nil
}

func normalizeOfficePreviewText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || unicode.IsPrint(r) {
			return r
		}
		return -1
	}, value)
	return strings.TrimSpace(strings.Join(strings.Fields(value), " "))
}

func xlsxSheetNames(files []*zip.File) ([]string, error) {
	workbook := findZipFile(files, "xl/workbook.xml")
	if workbook == nil {
		return nil, fmt.Errorf("XLSX is missing xl/workbook.xml")
	}
	stream, err := workbook.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	decoder := xml.NewDecoder(io.LimitReader(stream, officePreviewMaxText+1))
	var names []string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "sheet" {
			for _, attr := range start.Attr {
				if attr.Name.Local == "name" {
					names = append(names, attr.Value)
				}
			}
		}
	}
	return names, nil
}

func xlsxSharedStrings(files []*zip.File) ([]string, error) {
	part := findZipFile(files, "xl/sharedStrings.xml")
	if part == nil {
		return nil, nil
	}
	stream, err := part.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	decoder := xml.NewDecoder(io.LimitReader(stream, officePreviewMaxText+1))
	var values []string
	var current strings.Builder
	inItem, inText := false, false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", part.Name, err)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "si":
				inItem = true
				current.Reset()
			case "t":
				if inItem {
					inText = true
				}
			}
		case xml.CharData:
			if inText {
				current.Write([]byte(typed))
			}
		case xml.EndElement:
			switch typed.Name.Local {
			case "t":
				inText = false
			case "si":
				values = append(values, normalizeOfficePreviewText(current.String()))
				if len(values) >= 100_000 {
					return nil, fmt.Errorf("XLSX shared string table is too large")
				}
				inItem = false
			}
		}
	}
	return values, nil
}

func xlsxRows(file *zip.File, shared []string) ([][]string, error) {
	stream, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	decoder := xml.NewDecoder(io.LimitReader(stream, officePreviewMaxText+1))
	var rows [][]string
	var row []string
	var cellRef, cellType, value, inline, formula string
	var inValue, inText, inFormula, inCell bool
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", file.Name, err)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "row":
				row = nil
			case "c":
				inCell, cellRef, cellType, value, inline, formula = true, "", "", "", "", ""
				for _, attr := range typed.Attr {
					switch attr.Name.Local {
					case "r":
						cellRef = attr.Value
					case "t":
						cellType = attr.Value
					}
				}
			case "v":
				inValue = true
			case "t":
				if inCell {
					inText = true
				}
			case "f":
				inFormula = true
			}
		case xml.CharData:
			switch {
			case inValue:
				value += string(typed)
			case inText:
				inline += string(typed)
			case inFormula:
				formula += string(typed)
			}
		case xml.EndElement:
			switch typed.Name.Local {
			case "v":
				inValue = false
			case "t":
				inText = false
			case "f":
				inFormula = false
			case "c":
				column := xlsxColumnIndex(cellRef)
				if column >= 0 && column < 30 {
					for len(row) <= column {
						row = append(row, "")
					}
					display := inline
					if display == "" {
						display = value
					}
					if cellType == "s" {
						if index, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && index >= 0 && index < len(shared) {
							display = shared[index]
						}
					}
					if formula != "" {
						display = "=" + formula
					}
					row[column] = normalizeOfficePreviewText(display)
				}
				inCell = false
			case "row":
				rows = append(rows, row)
				if len(rows) >= 200 {
					return rows, nil
				}
			}
		}
	}
	return rows, nil
}

func xlsxColumnIndex(reference string) int {
	column := 0
	found := false
	for _, r := range reference {
		if r < 'A' || r > 'Z' {
			break
		}
		column = column*26 + int(r-'A'+1)
		found = true
	}
	if !found {
		return -1
	}
	return column - 1
}

func naturalPartLess(left, right string) bool {
	leftBase, rightBase := path.Base(left), path.Base(right)
	leftNumber, rightNumber := trailingNumber(leftBase), trailingNumber(rightBase)
	if leftNumber != rightNumber {
		return leftNumber < rightNumber
	}
	return left < right
}

func trailingNumber(value string) int {
	value = strings.TrimSuffix(value, path.Ext(value))
	index := len(value)
	for index > 0 && value[index-1] >= '0' && value[index-1] <= '9' {
		index--
	}
	number, _ := strconv.Atoi(value[index:])
	return number
}

func officePreviewHTML(title, styles, body string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` +
		html.EscapeString(title) + `</title><style>` + officePreviewBaseCSS() + styles +
		`</style></head><body>` + body + `</body></html>`
}

func officePreviewBaseCSS() string {
	return `:root{color-scheme:light;font-family:Inter,Aptos,Arial,sans-serif;color:#263238;background:#e9edf2}*{box-sizing:border-box}body{margin:0;padding:28px}h1,h2,p{overflow-wrap:anywhere}`
}

func officeDocumentCSS() string {
	return `.document-paper{max-width:820px;min-height:1060px;margin:0 auto;padding:72px 82px;background:#fff;box-shadow:0 12px 40px #26323826;border-radius:3px}.document-paper h1{margin:0 0 30px;color:#17365d;font-size:34px;line-height:1.2}.document-paper h2{margin:28px 0 10px;color:#1f4e78;font-size:20px}.document-paper p{margin:0 0 12px;font:15px/1.65 Georgia,serif;color:#263238}`
}

func officePresentationCSS() string {
	return `.deck{display:grid;gap:26px;max-width:1100px;margin:0 auto}.slide{position:relative;aspect-ratio:16/9;padding:7% 8%;overflow:hidden;background:#f7f9fc;border-left:18px solid #2e75b6;box-shadow:0 10px 32px #26323830}.slide h1{margin:0 0 7%;font-size:clamp(24px,4vw,48px);line-height:1.1;color:#17365d}.slide ul{margin:0;padding-left:1.2em;font-size:clamp(15px,2vw,25px);line-height:1.6}.slide-number{position:absolute;right:4%;bottom:4%;font-size:12px;color:#7a8793}`
}

func officeSpreadsheetCSS() string {
	return `.workbook{display:grid;gap:24px}.sheet{padding:18px;background:#fff;border-radius:8px;box-shadow:0 8px 24px #2632381f}.sheet h2{margin:0 0 12px;color:#17365d}.table-scroll{overflow:auto;max-height:650px;border:1px solid #d9e2f3}table{border-collapse:separate;border-spacing:0;min-width:100%;font-size:13px}th,td{min-width:110px;max-width:360px;padding:8px 10px;border-right:1px solid #d9e2f3;border-bottom:1px solid #d9e2f3;text-align:left;white-space:pre-wrap}th{position:sticky;top:0;background:#1f4e78;color:#fff;z-index:1}tr:nth-child(odd) td{background:#eaf2f8}`
}

package readers

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	officeReaderMaxXMLBytes    = 4 << 20
	officeReaderMaxOutputBytes = 4 << 20
	officeReaderMaxCellBytes   = 32 << 10
	officeReaderMaxCells       = 50_000
)

// OfficeReader extracts bounded text from DOCX, XLSX, and PPTX packages so the
// model can inspect and refine native documents without shelling out to Office.
type OfficeReader struct{}

func NewOfficeReader() *OfficeReader { return &OfficeReader{} }

func (r *OfficeReader) Read(filePath string) (string, error) {
	archive, err := zip.OpenReader(filePath)
	if err != nil {
		return "", fmt.Errorf("open Office package: %w", err)
	}
	defer archive.Close()
	if len(archive.File) == 0 || len(archive.File) > 2048 {
		return "", fmt.Errorf("Office package must contain 1-2048 parts")
	}
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".docx":
		return r.readDOCX(archive.File)
	case ".pptx":
		return r.readPPTX(archive.File)
	case ".xlsx":
		return r.readXLSX(archive.File)
	default:
		return "", fmt.Errorf("unsupported Office format")
	}
}

func (r *OfficeReader) readDOCX(files []*zip.File) (string, error) {
	document := officeFindPart(files, "word/document.xml")
	if document == nil {
		return "", fmt.Errorf("DOCX is missing word/document.xml")
	}
	paragraphs, err := officeParagraphs(document)
	if err != nil {
		return "", err
	}
	if len(paragraphs) == 0 {
		return "", fmt.Errorf("DOCX contains no extractable text")
	}
	var output strings.Builder
	officeAppendOutput(&output, "# DOCX Document\n\n")
	for index, paragraph := range paragraphs {
		if index > 0 && !officeAppendOutput(&output, "\n\n") {
			break
		}
		if !officeAppendOutput(&output, paragraph) {
			break
		}
	}
	return output.String(), nil
}

func (r *OfficeReader) readPPTX(files []*zip.File) (string, error) {
	var slides []*zip.File
	for _, file := range files {
		if strings.HasPrefix(file.Name, "ppt/slides/slide") &&
			strings.HasSuffix(file.Name, ".xml") &&
			!strings.Contains(file.Name, "/_rels/") {
			slides = append(slides, file)
		}
	}
	sort.Slice(slides, func(i, j int) bool {
		return officeTrailingNumber(slides[i].Name) < officeTrailingNumber(slides[j].Name)
	})
	if len(slides) == 0 {
		return "", fmt.Errorf("PPTX contains no slides")
	}
	var output strings.Builder
	officeAppendOutput(&output, "# PowerPoint Presentation\n")
	for index, slide := range slides {
		paragraphs, err := officeParagraphs(slide)
		if err != nil {
			return "", err
		}
		if !officeAppendOutput(&output, fmt.Sprintf("\n## Slide %d\n\n", index+1)) {
			return strings.TrimSpace(output.String()), nil
		}
		for _, paragraph := range paragraphs {
			if !officeAppendOutput(&output, paragraph+"\n") {
				return strings.TrimSpace(output.String()), nil
			}
		}
	}
	return strings.TrimSpace(output.String()), nil
}

func (r *OfficeReader) readXLSX(files []*zip.File) (string, error) {
	shared, err := officeSharedStrings(files)
	if err != nil {
		return "", err
	}
	names, err := officeWorkbookSheetNames(files)
	if err != nil {
		return "", err
	}
	var sheets []*zip.File
	for _, file := range files {
		if strings.HasPrefix(file.Name, "xl/worksheets/sheet") && strings.HasSuffix(file.Name, ".xml") {
			sheets = append(sheets, file)
		}
	}
	sort.Slice(sheets, func(i, j int) bool {
		return officeTrailingNumber(sheets[i].Name) < officeTrailingNumber(sheets[j].Name)
	})
	if len(sheets) == 0 {
		return "", fmt.Errorf("XLSX contains no worksheets")
	}
	var output strings.Builder
	officeAppendOutput(&output, "# Excel Workbook\n")
	for index, sheet := range sheets {
		rows, err := officeWorksheetRows(sheet, shared)
		if err != nil {
			return "", err
		}
		name := fmt.Sprintf("Sheet %d", index+1)
		if index < len(names) && names[index] != "" {
			name = names[index]
		}
		if !officeAppendOutput(&output, "\n## "+name+"\n\n") {
			return strings.TrimSpace(output.String()), nil
		}
		for rowIndex, row := range rows {
			if !officeAppendOutput(&output, strconv.Itoa(rowIndex+1)+"\t") {
				return strings.TrimSpace(output.String()), nil
			}
			for columnIndex, cell := range row {
				if columnIndex > 0 && !officeAppendOutput(&output, "\t") {
					return strings.TrimSpace(output.String()), nil
				}
				if !officeAppendOutput(&output, cell) {
					return strings.TrimSpace(output.String()), nil
				}
			}
			if !officeAppendOutput(&output, "\n") {
				return strings.TrimSpace(output.String()), nil
			}
		}
	}
	return strings.TrimSpace(output.String()), nil
}

func officeParagraphs(file *zip.File) ([]string, error) {
	stream, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	limited := &io.LimitedReader{R: stream, N: officeReaderMaxXMLBytes + 1}
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
			if typed.Name.Local == "t" {
				inText = true
			}
		case xml.EndElement:
			if typed.Name.Local == "t" {
				inText = false
			}
			if typed.Name.Local == "p" {
				if text := strings.TrimSpace(strings.Join(strings.Fields(current.String()), " ")); text != "" {
					paragraphs = append(paragraphs, text)
				}
				current.Reset()
			}
		case xml.CharData:
			if inText {
				current.Write([]byte(typed))
			}
		}
		if len(paragraphs) >= 4000 {
			break
		}
	}
	if limited.N == 0 {
		return nil, fmt.Errorf("%s exceeds the Office extraction limit", file.Name)
	}
	if text := strings.TrimSpace(strings.Join(strings.Fields(current.String()), " ")); text != "" {
		paragraphs = append(paragraphs, text)
	}
	return paragraphs, nil
}

func officeSharedStrings(files []*zip.File) ([]string, error) {
	part := officeFindPart(files, "xl/sharedStrings.xml")
	if part == nil {
		return nil, nil
	}
	stream, err := part.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	decoder := xml.NewDecoder(io.LimitReader(stream, officeReaderMaxXMLBytes+1))
	var values []string
	var current strings.Builder
	inItem, inText := false, false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == "si" {
				inItem = true
				current.Reset()
			}
			if inItem && typed.Name.Local == "t" {
				inText = true
			}
		case xml.CharData:
			if inText {
				current.Write([]byte(typed))
			}
		case xml.EndElement:
			if typed.Name.Local == "t" {
				inText = false
			}
			if typed.Name.Local == "si" {
				values = append(values, strings.TrimSpace(current.String()))
				inItem = false
			}
		}
	}
	return values, nil
}

func officeWorkbookSheetNames(files []*zip.File) ([]string, error) {
	part := officeFindPart(files, "xl/workbook.xml")
	if part == nil {
		return nil, fmt.Errorf("XLSX is missing xl/workbook.xml")
	}
	stream, err := part.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	decoder := xml.NewDecoder(io.LimitReader(stream, officeReaderMaxXMLBytes+1))
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

func officeWorksheetRows(file *zip.File, shared []string) ([][]string, error) {
	stream, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	decoder := xml.NewDecoder(io.LimitReader(stream, officeReaderMaxXMLBytes+1))
	var rows [][]string
	var row []string
	var ref, cellType, value, inline, formula string
	var inCell, inValue, inText, inFormula bool
	totalBytes, totalCells := 0, 0
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
				inCell, ref, cellType, value, inline, formula = true, "", "", "", "", ""
				for _, attr := range typed.Attr {
					if attr.Name.Local == "r" {
						ref = attr.Value
					} else if attr.Name.Local == "t" {
						cellType = attr.Value
					}
				}
			case "v":
				inValue = true
			case "t":
				inText = inCell
			case "f":
				inFormula = true
			}
		case xml.CharData:
			if inValue {
				value += string(typed)
			} else if inText {
				inline += string(typed)
			} else if inFormula {
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
				column := officeColumnIndex(ref)
				if column >= 0 && column < 100 {
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
						display = "=" + formula + " → " + value
					}
					display = strings.TrimSpace(display)
					display = officeUTF8Prefix(display, officeReaderMaxCellBytes)
					remaining := officeReaderMaxOutputBytes - totalBytes
					if remaining <= 0 || totalCells >= officeReaderMaxCells {
						return append(rows, row), nil
					}
					if len(display) > remaining {
						display = officeUTF8Prefix(display, remaining)
					}
					row[column] = display
					totalBytes += len(display)
					totalCells++
					if totalBytes >= officeReaderMaxOutputBytes || totalCells >= officeReaderMaxCells {
						return append(rows, row), nil
					}
				}
				inCell = false
			case "row":
				rows = append(rows, row)
				if len(rows) >= 5000 {
					return rows, nil
				}
			}
		}
	}
	return rows, nil
}

func officeAppendOutput(output *strings.Builder, text string) bool {
	remaining := officeReaderMaxOutputBytes - output.Len()
	if remaining <= 0 {
		return false
	}
	if len(text) <= remaining {
		output.WriteString(text)
		return true
	}
	const marker = "\n[Office extraction truncated]\n"
	contentBytes := remaining - len(marker)
	if contentBytes > 0 {
		output.WriteString(officeUTF8Prefix(text, contentBytes))
	}
	if output.Len()+len(marker) <= officeReaderMaxOutputBytes {
		output.WriteString(marker)
	}
	return false
}

func officeUTF8Prefix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func officeColumnIndex(reference string) int {
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

func officeFindPart(files []*zip.File, name string) *zip.File {
	for _, file := range files {
		if file.Name == name {
			return file
		}
	}
	return nil
}

func officeTrailingNumber(name string) int {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	index := len(base)
	for index > 0 && base[index-1] >= '0' && base[index-1] <= '9' {
		index--
	}
	number, _ := strconv.Atoi(base[index:])
	return number
}

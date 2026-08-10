package tools

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type deterministicZipWriter struct {
	writer *zip.Writer
}

func newDeterministicZipWriter(destination io.Writer) *deterministicZipWriter {
	return &deterministicZipWriter{writer: zip.NewWriter(destination)}
}

func (w *deterministicZipWriter) add(name string, data []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
	header.SetMode(0o600)
	file, err := w.writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	return err
}

func (w *deterministicZipWriter) close() error { return w.writer.Close() }

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func xmlText(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}

func ooxmlCoreProperties(spec DocumentSpec) string {
	now := time.Now().UTC().Format(time.RFC3339)
	author := xmlText(spec.Author)
	if author == "" {
		author = "Gokin Studio"
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" ` +
		`xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
		`<dc:title>` + xmlText(spec.Title) + `</dc:title><dc:creator>` + author + `</dc:creator>` +
		`<cp:lastModifiedBy>Gokin Studio</cp:lastModifiedBy>` +
		`<dcterms:created xsi:type="dcterms:W3CDTF">` + now + `</dcterms:created>` +
		`<dcterms:modified xsi:type="dcterms:W3CDTF">` + now + `</dcterms:modified>` +
		`</cp:coreProperties>`
}

func buildDOCX(spec DocumentSpec) ([]byte, error) {
	files := map[string][]byte{
		"[Content_Types].xml": []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
  <Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>
  <Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>
</Types>`),
		"_rels/.rels": []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>
</Relationships>`),
		"docProps/core.xml": []byte(ooxmlCoreProperties(spec)),
		"docProps/app.xml": []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">
  <Application>Gokin Studio</Application><AppVersion>1.0</AppVersion>
</Properties>`),
		"word/_rels/document.xml.rels": []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`),
		"word/styles.xml":   []byte(docxStylesXML()),
		"word/document.xml": []byte(docxDocumentXML(spec)),
	}
	return buildZipPackage(files)
}

func docxDocumentXML(spec DocumentSpec) string {
	var body strings.Builder
	body.WriteString(docxParagraph(spec.Title, "Title", false, false))
	if spec.Subtitle != "" {
		body.WriteString(docxParagraph(spec.Subtitle, "Subtitle", false, false))
	}
	for _, section := range spec.Sections {
		if section.Heading != "" {
			body.WriteString(docxParagraph(section.Heading, "Heading"+strconv.Itoa(section.Level), false, false))
		}
		for _, paragraph := range section.Paragraphs {
			body.WriteString(docxParagraph(paragraph, "BodyText", false, false))
		}
		for _, bullet := range section.Bullets {
			body.WriteString(docxParagraph("•  "+bullet, "ListParagraph", false, false))
		}
		if section.Table != nil {
			body.WriteString(docxTableXML(*section.Table))
		}
	}
	body.WriteString(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134" w:header="708" w:footer="708" w:gutter="0"/></w:sectPr>`)
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body>` + body.String() + `</w:body></w:document>`
}

func docxParagraph(text, style string, bold, italic bool) string {
	properties := ""
	if style != "" {
		properties = `<w:pPr><w:pStyle w:val="` + xmlText(style) + `"/></w:pPr>`
	}
	runProps := `<w:rPr><w:lang w:val="en-US" w:eastAsia="en-US"/>`
	if bold {
		runProps += `<w:b/>`
	}
	if italic {
		runProps += `<w:i/>`
	}
	runProps += `</w:rPr>`
	return `<w:p>` + properties + `<w:r>` + runProps + `<w:t xml:space="preserve">` + xmlText(text) + `</w:t></w:r></w:p>`
}

func docxTableXML(table DocumentTable) string {
	var result strings.Builder
	result.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="GokinTable"/><w:tblW w:w="0" w:type="auto"/><w:tblLook w:val="04A0" w:firstRow="1" w:lastRow="0" w:firstColumn="0" w:lastColumn="0" w:noHBand="0" w:noVBand="1"/></w:tblPr>`)
	writeRow := func(values []string, header bool) {
		result.WriteString(`<w:tr>`)
		for _, value := range values {
			shading := ""
			if header {
				shading = `<w:shd w:val="clear" w:color="auto" w:fill="1F4E78"/>`
			}
			result.WriteString(`<w:tc><w:tcPr><w:tcW w:w="0" w:type="auto"/>` + shading + `</w:tcPr>`)
			if header {
				result.WriteString(docxParagraph(value, "", true, false))
			} else {
				result.WriteString(docxParagraph(value, "", false, false))
			}
			result.WriteString(`</w:tc>`)
		}
		result.WriteString(`</w:tr>`)
	}
	writeRow(table.Headers, true)
	for _, row := range table.Rows {
		writeRow(row, false)
	}
	result.WriteString(`</w:tbl><w:p/>`)
	return result.String()
}

func docxStylesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:docDefaults>
    <w:rPrDefault><w:rPr><w:rFonts w:ascii="Aptos" w:hAnsi="Aptos" w:eastAsia="Arial"/><w:sz w:val="22"/><w:szCs w:val="22"/><w:color w:val="263238"/></w:rPr></w:rPrDefault>
    <w:pPrDefault><w:pPr><w:spacing w:after="160" w:line="276" w:lineRule="auto"/></w:pPr></w:pPrDefault>
  </w:docDefaults>
  <w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>
  <w:style w:type="paragraph" w:styleId="BodyText"><w:name w:val="Body Text"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:after="180"/></w:pPr></w:style>
  <w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:next w:val="Subtitle"/><w:qFormat/><w:pPr><w:spacing w:before="240" w:after="180"/></w:pPr><w:rPr><w:rFonts w:ascii="Aptos Display" w:hAnsi="Aptos Display"/><w:b/><w:color w:val="17365D"/><w:sz w:val="52"/><w:szCs w:val="52"/></w:rPr></w:style>
  <w:style w:type="paragraph" w:styleId="Subtitle"><w:name w:val="Subtitle"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:after="360"/></w:pPr><w:rPr><w:i/><w:color w:val="5B6573"/><w:sz w:val="26"/></w:rPr></w:style>
  <w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="BodyText"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="320" w:after="120"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:color w:val="1F4E78"/><w:sz w:val="34"/></w:rPr></w:style>
  <w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:next w:val="BodyText"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="260" w:after="100"/><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:b/><w:color w:val="2E75B6"/><w:sz w:val="28"/></w:rPr></w:style>
  <w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:next w:val="BodyText"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="220" w:after="80"/><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:b/><w:color w:val="365F91"/><w:sz w:val="24"/></w:rPr></w:style>
  <w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/><w:basedOn w:val="BodyText"/><w:pPr><w:ind w:left="360" w:hanging="240"/></w:pPr></w:style>
  <w:style w:type="table" w:styleId="GokinTable"><w:name w:val="Gokin Table"/><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="4" w:color="B4C6E7"/><w:left w:val="single" w:sz="4" w:color="B4C6E7"/><w:bottom w:val="single" w:sz="4" w:color="B4C6E7"/><w:right w:val="single" w:sz="4" w:color="B4C6E7"/><w:insideH w:val="single" w:sz="4" w:color="D9E2F3"/><w:insideV w:val="single" w:sz="4" w:color="D9E2F3"/></w:tblBorders><w:tblCellMar><w:top w:w="80" w:type="dxa"/><w:left w:w="100" w:type="dxa"/><w:bottom w:w="80" w:type="dxa"/><w:right w:w="100" w:type="dxa"/></w:tblCellMar></w:tblPr></w:style>
</w:styles>`
}

func buildXLSX(spec DocumentSpec) ([]byte, error) {
	contentTypes := strings.Builder{}
	contentTypes.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/><Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>`)
	workbook := strings.Builder{}
	workbook.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><bookViews><workbookView/></bookViews><sheets>`)
	workbookRels := strings.Builder{}
	workbookRels.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	files := map[string][]byte{}
	for index, sheet := range spec.Sheets {
		id := index + 1
		contentTypes.WriteString(fmt.Sprintf(`<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, id))
		workbook.WriteString(fmt.Sprintf(`<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlText(sheet.Name), id, id))
		workbookRels.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, id, id))
		files[fmt.Sprintf("xl/worksheets/sheet%d.xml", id)] = []byte(xlsxSheetXML(sheet))
	}
	styleID := len(spec.Sheets) + 1
	workbookRels.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, styleID))
	workbookRels.WriteString(`</Relationships>`)
	workbook.WriteString(`</sheets><calcPr calcId="191029" fullCalcOnLoad="1"/></workbook>`)
	contentTypes.WriteString(`</Types>`)
	files["[Content_Types].xml"] = []byte(contentTypes.String())
	files["_rels/.rels"] = []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>
</Relationships>`)
	files["docProps/core.xml"] = []byte(ooxmlCoreProperties(spec))
	files["docProps/app.xml"] = []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><Application>Gokin Studio</Application><TitlesOfParts><vt:vector size="%d" baseType="lpstr">%s</vt:vector></TitlesOfParts></Properties>`, len(spec.Sheets), xlsxSheetTitles(spec.Sheets)))
	files["xl/workbook.xml"] = []byte(workbook.String())
	files["xl/_rels/workbook.xml.rels"] = []byte(workbookRels.String())
	files["xl/styles.xml"] = []byte(xlsxStylesXML())
	return buildZipPackage(files)
}

func xlsxSheetTitles(sheets []SpreadsheetSheet) string {
	var result strings.Builder
	for _, sheet := range sheets {
		result.WriteString(`<vt:lpstr>` + xmlText(sheet.Name) + `</vt:lpstr>`)
	}
	return result.String()
}

func xlsxSheetXML(sheet SpreadsheetSheet) string {
	maxColumns := 0
	widths := make([]float64, maxSpreadsheetColumns)
	for _, row := range sheet.Rows {
		if len(row) > maxColumns {
			maxColumns = len(row)
		}
		for column, value := range row {
			length := math.Min(60, float64(utf8.RuneCountInString(spreadsheetDisplayValue(value)))+2)
			if length > widths[column] {
				widths[column] = length
			}
		}
	}
	var result strings.Builder
	result.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView tabSelected="1" workbookViewId="0">`)
	if len(sheet.Rows) > 1 {
		result.WriteString(`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>`)
	}
	result.WriteString(`</sheetView></sheetViews><sheetFormatPr defaultRowHeight="18"/><cols>`)
	for column := 0; column < maxColumns; column++ {
		width := math.Max(10, widths[column])
		result.WriteString(fmt.Sprintf(`<col min="%d" max="%d" width="%.1f" customWidth="1"/>`, column+1, column+1, width))
	}
	result.WriteString(`</cols><sheetData>`)
	for rowIndex, row := range sheet.Rows {
		result.WriteString(fmt.Sprintf(`<row r="%d"%s>`, rowIndex+1, map[bool]string{true: ` ht="22" customHeight="1"`, false: ""}[rowIndex == 0]))
		for columnIndex, value := range row {
			ref := spreadsheetColumnName(columnIndex+1) + strconv.Itoa(rowIndex+1)
			style := 0
			if rowIndex == 0 {
				style = 1
			} else if rowIndex%2 == 0 {
				style = 2
			}
			result.WriteString(xlsxCellXML(ref, value, style))
		}
		result.WriteString(`</row>`)
	}
	result.WriteString(`</sheetData>`)
	if len(sheet.Rows) > 1 && maxColumns > 0 {
		result.WriteString(`<autoFilter ref="A1:` + spreadsheetColumnName(maxColumns) + strconv.Itoa(len(sheet.Rows)) + `"/>`)
	}
	result.WriteString(`<pageMargins left="0.25" right="0.25" top="0.5" bottom="0.5" header="0.2" footer="0.2"/></worksheet>`)
	return result.String()
}

func xlsxCellXML(ref string, value any, style int) string {
	styleAttr := ""
	if style > 0 {
		styleAttr = fmt.Sprintf(` s="%d"`, style)
	}
	switch typed := value.(type) {
	case nil:
		return `<c r="` + ref + `"` + styleAttr + `/>`
	case bool:
		number := "0"
		if typed {
			number = "1"
		}
		return `<c r="` + ref + `" t="b"` + styleAttr + `><v>` + number + `</v></c>`
	case json.Number:
		if _, err := typed.Float64(); err == nil {
			return `<c r="` + ref + `"` + styleAttr + `><v>` + xmlText(string(typed)) + `</v></c>`
		}
	case string:
		if strings.HasPrefix(typed, "=") {
			return `<c r="` + ref + `"` + styleAttr + `><f>` + xmlText(strings.TrimPrefix(typed, "=")) + `</f><v>0</v></c>`
		}
		return `<c r="` + ref + `" t="inlineStr"` + styleAttr + `><is><t xml:space="preserve">` + xmlText(typed) + `</t></is></c>`
	}
	return `<c r="` + ref + `" t="inlineStr"` + styleAttr + `><is><t>` + xmlText(fmt.Sprint(value)) + `</t></is></c>`
}

func spreadsheetDisplayValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func spreadsheetColumnName(column int) string {
	var result string
	for column > 0 {
		column--
		result = string(rune('A'+column%26)) + result
		column /= 26
	}
	return result
}

func xlsxStylesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <fonts count="2"><font><sz val="11"/><color rgb="FF263238"/><name val="Aptos"/></font><font><b/><sz val="11"/><color rgb="FFFFFFFF"/><name val="Aptos"/></font></fonts>
  <fills count="4"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FF1F4E78"/><bgColor indexed="64"/></patternFill></fill><fill><patternFill patternType="solid"><fgColor rgb="FFEAF2F8"/><bgColor indexed="64"/></patternFill></fill></fills>
  <borders count="2"><border><left/><right/><top/><bottom/><diagonal/></border><border><left style="thin"><color rgb="FFD9E2F3"/></left><right style="thin"><color rgb="FFD9E2F3"/></right><top style="thin"><color rgb="FFD9E2F3"/></top><bottom style="thin"><color rgb="FFD9E2F3"/></bottom><diagonal/></border></borders>
  <cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
  <cellXfs count="3"><xf numFmtId="0" fontId="0" fillId="0" borderId="1" xfId="0" applyBorder="1" applyAlignment="1"><alignment vertical="center" wrapText="1"/></xf><xf numFmtId="0" fontId="1" fillId="2" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="center" wrapText="1"/></xf><xf numFmtId="0" fontId="0" fillId="3" borderId="1" xfId="0" applyFill="1" applyBorder="1" applyAlignment="1"><alignment vertical="center" wrapText="1"/></xf></cellXfs>
  <cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>
  <dxfs count="0"/><tableStyles count="0" defaultTableStyle="TableStyleMedium2" defaultPivotStyle="PivotStyleLight16"/>
</styleSheet>`
}

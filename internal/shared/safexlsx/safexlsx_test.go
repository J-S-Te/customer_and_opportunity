package safexlsx

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestParseWorkbookReadsFirstVisibleSheetAndCellTypes(t *testing.T) {
	files := minimumWorkbookFiles(`
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
  <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="inlineStr"><is><t>内联</t></is></c><c r="C1" t="b"><v>1</v></c></row>
  <row r="2"><c r="A2"><f>1+1</f><v>2</v></c><c r="B2" t="str"><v>文本</v></c><c r="C2" t="n"><v>42.5</v></c></row>
</sheetData></worksheet>`)
	files = replaceFixture(files, "xl/sharedStrings.xml", `<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><r><t>共享</t></r><r><t>字符串</t></r></si></sst>`)
	data := makeZip(t, files)

	rows, err := ParseWorkbook(data, DefaultLimits())
	if err != nil {
		t.Fatalf("ParseWorkbook() error = %v", err)
	}
	if len(rows) != 2 || len(rows[0]) != 3 || len(rows[1]) != 3 {
		t.Fatalf("unexpected workbook shape: %#v", rows)
	}
	if rows[0][0].Value != "共享字符串" || rows[0][1].Value != "内联" || rows[0][2].Value != "TRUE" {
		t.Fatalf("unexpected first row: %#v", rows[0])
	}
	if rows[1][0].Value != "2" || !rows[1][0].Formula {
		t.Fatalf("formula was evaluated or not marked: %#v", rows[1][0])
	}
	if rows[1][1].Value != "文本" || rows[1][2].Value != "42.5" {
		t.Fatalf("unexpected second row: %#v", rows[1])
	}
}

func TestParseWorkbookUsesFirstVisibleSheet(t *testing.T) {
	files := minimumWorkbookFiles(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>visible</t></is></c></row></sheetData></worksheet>`)
	for index := range files {
		switch files[index].name {
		case workbookPath:
			files[index].data = `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="hidden" state="hidden" sheetId="1" r:id="rIdHidden"/><sheet name="visible" sheetId="2" r:id="rIdSheet"/></sheets></workbook>`
		case workbookRelsPath:
			files[index].data = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdHidden" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/hidden.xml"/><Relationship Id="rIdSheet" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rIdShared" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/></Relationships>`
		case contentTypesPath:
			files[index].data = strings.Replace(files[index].data, `</Types>`, `<Override PartName="/xl/worksheets/hidden.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`, 1)
		}
	}
	files = append(files, zipFixture{name: "xl/worksheets/hidden.xml", data: `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>hidden</t></is></c></row></sheetData></worksheet>`})
	rows, err := ParseWorkbook(makeZip(t, files), DefaultLimits())
	if err != nil || rows[0][0].Value != "visible" {
		t.Fatalf("ParseWorkbook() rows=%#v err=%v", rows, err)
	}
}

func TestParseWorkbookRejectsUnsafePackages(t *testing.T) {
	valid := minimumWorkbookFiles(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>`)
	tests := map[string][]zipFixture{
		"path traversal":                appendCopy(valid, zipFixture{name: "../secret", data: "secret"}),
		"backslash path":                appendCopy(valid, zipFixture{name: `xl\unsafe.xml`, data: "unsafe"}),
		"duplicate critical entry":      appendCopy(valid, zipFixture{name: workbookPath, data: "duplicate"}),
		"case duplicate critical entry": appendCopy(valid, zipFixture{name: "XL/workbook.xml", data: "duplicate"}),
		"macro part":                    appendCopy(valid, zipFixture{name: "xl/vbaProject.bin", data: "macro"}),
		"external link part":            appendCopy(valid, zipFixture{name: "xl/externalLinks/externalLink1.xml", data: "external"}),
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseWorkbook(makeZip(t, files), DefaultLimits()); err == nil {
				t.Fatal("ParseWorkbook() error = nil")
			}
		})
	}
}

func TestParseWorkbookRejectsMacroContentTypeAndExternalRelationship(t *testing.T) {
	tests := map[string]func([]zipFixture) []zipFixture{
		"xlsm content type": func(files []zipFixture) []zipFixture {
			return replaceFixture(files, contentTypesPath, strings.Replace(findFixture(files, contentTypesPath), xlsxWorkbookType, "application/vnd.ms-excel.sheet.macroEnabled.main+xml", 1))
		},
		"external relationship": func(files []zipFixture) []zipFixture {
			return replaceFixture(files, workbookRelsPath, strings.Replace(findFixture(files, workbookRelsPath), `Target="worksheets/sheet1.xml"`, `Target="https://attacker.invalid/book.xlsx" TargetMode="External"`, 1))
		},
		"relationship traversal": func(files []zipFixture) []zipFixture {
			return replaceFixture(files, workbookRelsPath, strings.Replace(findFixture(files, workbookRelsPath), `Target="worksheets/sheet1.xml"`, `Target="../worksheet.xml"`, 1))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			files := minimumWorkbookFiles(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>`)
			if _, err := ParseWorkbook(makeZip(t, mutate(files)), DefaultLimits()); err == nil {
				t.Fatal("ParseWorkbook() error = nil")
			}
		})
	}
}

func TestParseWorkbookRejectsConfiguredLimits(t *testing.T) {
	tests := []struct {
		name      string
		worksheet string
		limits    Limits
	}{
		{name: "row", worksheet: `<worksheet><sheetData><row r="3"><c r="A3"><v>1</v></c></row></sheetData></worksheet>`, limits: Limits{MaxRows: 2}},
		{name: "column", worksheet: `<worksheet><sheetData><row r="1"><c r="C1"><v>1</v></c></row></sheetData></worksheet>`, limits: Limits{MaxColumns: 2}},
		{name: "cell runes", worksheet: `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>三个字</t></is></c></row></sheetData></worksheet>`, limits: Limits{MaxCellRunes: 2}},
		{name: "mismatched coordinate", worksheet: `<worksheet><sheetData><row r="1"><c r="A2"><v>1</v></c></row></sheetData></worksheet>`, limits: DefaultLimits()},
		{name: "sparse coordinate", worksheet: `<worksheet><sheetData><row r="999999"><c r="A999999"><v>1</v></c></row></sheetData></worksheet>`, limits: DefaultLimits()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseWorkbook(makeZip(t, minimumWorkbookFiles(test.worksheet)), test.limits); err == nil {
				t.Fatal("ParseWorkbook() error = nil")
			}
		})
	}
}

func TestParseWorkbookRejectsArchiveAndExpansionLimits(t *testing.T) {
	data := makeZip(t, minimumWorkbookFiles(`<worksheet><sheetData/></worksheet>`))
	if _, err := ParseWorkbook(data, Limits{MaxArchiveBytes: int64(len(data) - 1)}); err == nil {
		t.Fatal("archive byte limit was not enforced")
	}
	if _, err := ParseWorkbook(data, Limits{MaxEntries: 2}); err == nil {
		t.Fatal("entry limit was not enforced")
	}
	if _, err := ParseWorkbook(data, Limits{MaxUncompressedBytes: 32}); err == nil {
		t.Fatal("uncompressed byte limit was not enforced")
	}
}

func TestParseWorkbookRejectsEncryptedFlag(t *testing.T) {
	data := makeZip(t, minimumWorkbookFiles(`<worksheet><sheetData/></worksheet>`))
	for offset := 0; offset+30 <= len(data); offset++ {
		if binary.LittleEndian.Uint32(data[offset:offset+4]) == 0x04034b50 {
			flags := binary.LittleEndian.Uint16(data[offset+6 : offset+8])
			binary.LittleEndian.PutUint16(data[offset+6:offset+8], flags|1)
		}
		if binary.LittleEndian.Uint32(data[offset:offset+4]) == 0x02014b50 {
			flags := binary.LittleEndian.Uint16(data[offset+8 : offset+10])
			binary.LittleEndian.PutUint16(data[offset+8:offset+10], flags|1)
		}
	}
	if _, err := ParseWorkbook(data, DefaultLimits()); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("ParseWorkbook() encrypted error = %v", err)
	}
}

func TestParseWorkbookRejectsDTDWithoutLeakingCellValue(t *testing.T) {
	secret := "TOP-SECRET-CUSTOMER-PHONE"
	worksheet := `<!DOCTYPE worksheet [<!ENTITY leak "` + secret + `">]><worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>&leak;</t></is></c></row></sheetData></worksheet>`
	_, err := ParseWorkbook(makeZip(t, minimumWorkbookFiles(worksheet)), DefaultLimits())
	if err == nil {
		t.Fatal("ParseWorkbook() accepted a DTD")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked a cell value: %v", err)
	}
}

func TestParseWorkbookRejectsDuplicateSheetDataAndTrailingRoot(t *testing.T) {
	tests := map[string]string{
		"duplicate sheet data": `<worksheet><sheetData/><sheetData/></worksheet>`,
		"trailing root":        `<worksheet><sheetData/></worksheet><worksheet/>`,
	}
	for name, worksheet := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseWorkbook(makeZip(t, minimumWorkbookFiles(worksheet)), DefaultLimits()); err == nil {
				t.Fatal("ParseWorkbook() error = nil")
			}
		})
	}
}

func TestParseWorkbookRejectsTrailingSharedStringsRoot(t *testing.T) {
	files := minimumWorkbookFiles(`<worksheet><sheetData/></worksheet>`)
	files = replaceFixture(files, "xl/sharedStrings.xml", `<sst></sst><sst></sst>`)
	if _, err := ParseWorkbook(makeZip(t, files), DefaultLimits()); err == nil {
		t.Fatal("ParseWorkbook() accepted trailing shared strings root")
	}
}

func TestParseWorkbookRejectsNonZipAndInvalidLimits(t *testing.T) {
	if _, err := ParseWorkbook([]byte("legacy-xls"), DefaultLimits()); err == nil {
		t.Fatal("ParseWorkbook() accepted non-zip input")
	}
	if _, err := ParseWorkbook([]byte("PK\x03\x04"), Limits{MaxRows: -1}); err == nil {
		t.Fatal("ParseWorkbook() accepted invalid limits")
	}
}

type zipFixture struct {
	name string
	data string
}

func minimumWorkbookFiles(worksheet string) []zipFixture {
	if !strings.Contains(worksheet, `xmlns=`) {
		worksheet = strings.Replace(worksheet, `<worksheet`, `<worksheet xmlns="`+spreadsheetNamespace+`"`, 1)
	}
	return []zipFixture{
		{name: contentTypesPath, data: `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="` + xlsxWorkbookType + `"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="` + xlsxWorksheetType + `"/><Override PartName="/xl/sharedStrings.xml" ContentType="` + xlsxSharedStringsType + `"/></Types>`},
		{name: rootRelsPath, data: `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{name: workbookPath, data: `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet1" sheetId="1" r:id="rIdSheet"/></sheets></workbook>`},
		{name: workbookRelsPath, data: `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdSheet" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rIdShared" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/></Relationships>`},
		{name: "xl/worksheets/sheet1.xml", data: worksheet},
		{name: "xl/sharedStrings.xml", data: `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"></sst>`},
	}
}

func makeZip(t *testing.T, files []zipFixture) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, file := range files {
		entry, err := writer.Create(file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write([]byte(file.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func appendCopy(files []zipFixture, file zipFixture) []zipFixture {
	result := append([]zipFixture(nil), files...)
	return append(result, file)
}

func findFixture(files []zipFixture, name string) string {
	for _, file := range files {
		if file.name == name {
			return file.data
		}
	}
	return ""
}

func replaceFixture(files []zipFixture, name, data string) []zipFixture {
	result := append([]zipFixture(nil), files...)
	for index := range result {
		if result[index].name == name {
			result[index].data = data
		}
	}
	return result
}

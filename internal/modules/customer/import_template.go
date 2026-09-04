package customer

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

const customerImportTemplateContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

var customerImportTemplateExample = []string{
	"示例科技有限公司", "913100001234567890", "企业", "软件", "华东", "请填写负责人用户ID", "请填写负责人组织ID", "张三", "13800138000", "zhangsan@example.com",
}

// customerImportTemplateWorkbook produces the same macro-free OOXML shape the
// import parser accepts. Keeping the header source shared with import.go
// prevents the downloadable template from drifting from the server contract.
func customerImportTemplateWorkbook() ([]byte, error) {
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	entries := map[string][]byte{
		"[Content_Types].xml":        []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`),
		"_rels/.rels":                []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`),
		"xl/workbook.xml":            []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="客户导入模板" sheetId="1" state="visible" r:id="rId1"/></sheets></workbook>`),
		"xl/_rels/workbook.xml.rels": []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`),
	}
	worksheet, err := customerImportTemplateWorksheet()
	if err != nil {
		return nil, err
	}
	entries["xl/worksheets/sheet1.xml"] = worksheet
	for name, contents := range entries {
		writer, err := archive.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err = writer.Write(contents); err != nil {
			return nil, err
		}
	}
	if err = archive.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func customerImportTemplateWorksheet() ([]byte, error) {
	var document strings.Builder
	document.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for rowIndex, row := range [][]string{importHeaders, customerImportTemplateExample} {
		document.WriteString(fmt.Sprintf(`<row r="%d">`, rowIndex+1))
		for columnIndex, value := range row {
			document.WriteString(fmt.Sprintf(`<c r="%s%d" t="inlineStr"><is><t>`, xlsxColumnName(columnIndex), rowIndex+1))
			if err := xml.EscapeText(&document, []byte(value)); err != nil {
				return nil, err
			}
			document.WriteString(`</t></is></c>`)
		}
		document.WriteString(`</row>`)
	}
	document.WriteString(`</sheetData></worksheet>`)
	return []byte(document.String()), nil
}

func xlsxColumnName(index int) string {
	return string(rune('A' + index))
}

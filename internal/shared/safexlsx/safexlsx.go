// Package safexlsx provides a deliberately small, fail-closed OOXML reader for
// customer imports. It does not evaluate formulas or support legacy/macro-enabled
// workbooks.
package safexlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	contentTypesPath      = "[Content_Types].xml"
	rootRelsPath          = "_rels/.rels"
	workbookPath          = "xl/workbook.xml"
	workbookRelsPath      = "xl/_rels/workbook.xml.rels"
	spreadsheetNamespace  = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	contentTypesNamespace = "http://schemas.openxmlformats.org/package/2006/content-types"
	relationshipNamespace = "http://schemas.openxmlformats.org/package/2006/relationships"
	xlsxWorkbookType      = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"
	xlsxWorksheetType     = "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"
	xlsxSharedStringsType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"
)

// Limits bounds both archive expansion and the dense result returned by
// ParseWorkbook. Zero values select the corresponding DefaultLimits value.
type Limits struct {
	MaxArchiveBytes      int64
	MaxEntries           int
	MaxUncompressedBytes uint64
	MaxRows              int
	MaxColumns           int
	MaxCellRunes         int
}

// Cell is a textual workbook cell. Formula is true when the source cell has an
// OOXML formula element. Value is the stored/cached value; formulas are never
// interpreted or executed.
type Cell struct {
	Value   string
	Formula bool
}

// DefaultLimits returns conservative import limits suitable for an HTTP upload.
func DefaultLimits() Limits {
	return Limits{
		MaxArchiveBytes:      10 << 20,
		MaxEntries:           2_000,
		MaxUncompressedBytes: 64 << 20,
		MaxRows:              20_000,
		MaxColumns:           256,
		MaxCellRunes:         8_192,
	}
}

// ParseWorkbook reads the first visible worksheet of a genuine .xlsx OOXML
// package. It rejects macro-enabled/encrypted/external-link packages and never
// evaluates formulas.
func ParseWorkbook(data []byte, limits Limits) ([][]Cell, error) {
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || int64(len(data)) > limits.MaxArchiveBytes {
		return nil, errors.New("xlsx archive size is invalid")
	}
	if len(data) < 4 || !bytes.Equal(data[:4], []byte{'P', 'K', 3, 4}) {
		return nil, errors.New("file is not an xlsx OOXML archive")
	}

	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("file is not a valid xlsx OOXML archive")
	}
	entries, err := indexEntries(reader, limits)
	if err != nil {
		return nil, err
	}

	contentData, err := readRequired(entries, contentTypesPath, limits.MaxUncompressedBytes)
	if err != nil {
		return nil, err
	}
	contentTypes, err := parseContentTypes(contentData)
	if err != nil {
		return nil, err
	}
	if err := validatePackageTypes(contentTypes, entries); err != nil {
		return nil, err
	}
	rootRelsData, err := readRequired(entries, rootRelsPath, limits.MaxUncompressedBytes)
	if err != nil {
		return nil, err
	}
	rootRels, err := parseRelationships(rootRelsData)
	if err != nil {
		return nil, err
	}
	if err := validateRootRelationships(rootRels); err != nil {
		return nil, err
	}

	workbookData, err := readRequired(entries, workbookPath, limits.MaxUncompressedBytes)
	if err != nil {
		return nil, err
	}
	workbook, err := parseWorkbook(workbookData)
	if err != nil {
		return nil, err
	}
	visibleRelID, err := firstVisibleSheet(workbook)
	if err != nil {
		return nil, err
	}

	relsData, err := readRequired(entries, workbookRelsPath, limits.MaxUncompressedBytes)
	if err != nil {
		return nil, err
	}
	rels, err := parseRelationships(relsData)
	if err != nil {
		return nil, err
	}
	worksheetPath, sharedStringsPath, err := resolveWorkbookRelationships(rels, visibleRelID)
	if err != nil {
		return nil, err
	}
	if !contentTypes.hasType(worksheetPath, xlsxWorksheetType) {
		return nil, errors.New("visible worksheet has an invalid OOXML content type")
	}

	var sharedStrings []string
	if sharedStringsPath != "" {
		if !contentTypes.hasType(sharedStringsPath, xlsxSharedStringsType) {
			return nil, errors.New("shared strings have an invalid OOXML content type")
		}
		sharedData, readErr := readRequired(entries, sharedStringsPath, limits.MaxUncompressedBytes)
		if readErr != nil {
			return nil, readErr
		}
		sharedStrings, err = parseSharedStrings(sharedData, limits)
		if err != nil {
			return nil, err
		}
	}

	worksheetData, err := readRequired(entries, worksheetPath, limits.MaxUncompressedBytes)
	if err != nil {
		return nil, err
	}
	return parseWorksheet(worksheetData, sharedStrings, limits)
}

func normalizeLimits(limits Limits) (Limits, error) {
	defaults := DefaultLimits()
	if limits.MaxArchiveBytes == 0 {
		limits.MaxArchiveBytes = defaults.MaxArchiveBytes
	}
	if limits.MaxEntries == 0 {
		limits.MaxEntries = defaults.MaxEntries
	}
	if limits.MaxUncompressedBytes == 0 {
		limits.MaxUncompressedBytes = defaults.MaxUncompressedBytes
	}
	if limits.MaxRows == 0 {
		limits.MaxRows = defaults.MaxRows
	}
	if limits.MaxColumns == 0 {
		limits.MaxColumns = defaults.MaxColumns
	}
	if limits.MaxCellRunes == 0 {
		limits.MaxCellRunes = defaults.MaxCellRunes
	}
	if limits.MaxArchiveBytes < 1 || limits.MaxEntries < 1 || limits.MaxRows < 1 || limits.MaxColumns < 1 || limits.MaxCellRunes < 1 {
		return Limits{}, errors.New("xlsx limits are invalid")
	}
	return limits, nil
}

func indexEntries(reader *zip.Reader, limits Limits) (map[string]*zip.File, error) {
	if len(reader.File) == 0 || len(reader.File) > limits.MaxEntries {
		return nil, errors.New("xlsx archive entry limit exceeded")
	}
	entries := make(map[string]*zip.File, len(reader.File))
	caseNames := make(map[string]string, len(reader.File))
	var total uint64
	for _, file := range reader.File {
		name := file.Name
		if !safeEntryName(name) {
			return nil, errors.New("xlsx archive contains an unsafe path")
		}
		lowerName := strings.ToLower(name)
		if previous, exists := caseNames[lowerName]; exists {
			_ = previous
			return nil, errors.New("xlsx archive contains duplicate entries")
		}
		caseNames[lowerName] = name
		if file.Flags&1 != 0 {
			return nil, errors.New("encrypted xlsx archives are not supported")
		}
		if !strings.HasSuffix(name, "/") && file.Method != zip.Store && file.Method != zip.Deflate {
			return nil, errors.New("xlsx archive uses an unsupported compression method")
		}
		if file.Mode()&(^file.Mode().Perm()) != 0 && !file.Mode().IsRegular() && !file.Mode().IsDir() {
			return nil, errors.New("xlsx archive contains a non-regular entry")
		}
		if math.MaxUint64-total < file.UncompressedSize64 {
			return nil, errors.New("xlsx uncompressed size limit exceeded")
		}
		total += file.UncompressedSize64
		if total > limits.MaxUncompressedBytes {
			return nil, errors.New("xlsx uncompressed size limit exceeded")
		}
		if forbiddenPart(lowerName) {
			return nil, errors.New("macro-enabled or externally linked workbooks are not supported")
		}
		if !strings.HasSuffix(name, "/") {
			entries[name] = file
		}
	}
	return entries, nil
}

func safeEntryName(name string) bool {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return false
	}
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == "" || path.Clean(trimmed) != trimmed {
		return false
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func forbiddenPart(lowerName string) bool {
	return strings.HasPrefix(lowerName, "xl/externallinks/") ||
		strings.HasPrefix(lowerName, "xl/vba") ||
		strings.HasSuffix(lowerName, "/vbaproject.bin") ||
		strings.Contains(lowerName, "macrosheets/")
}

func readRequired(entries map[string]*zip.File, name string, limit uint64) ([]byte, error) {
	file, exists := entries[name]
	if !exists {
		return nil, fmt.Errorf("required xlsx OOXML part %q is missing", name)
	}
	if file.UncompressedSize64 > limit || file.UncompressedSize64 >= uint64(maxInt()) {
		return nil, errors.New("xlsx OOXML part size limit exceeded")
	}
	stream, err := file.Open()
	if err != nil {
		return nil, errors.New("xlsx OOXML part cannot be opened")
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, int64(file.UncompressedSize64)+1))
	if err != nil {
		return nil, errors.New("xlsx OOXML part cannot be read")
	}
	if uint64(len(data)) != file.UncompressedSize64 {
		return nil, errors.New("xlsx OOXML part size is inconsistent")
	}
	return data, nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

type contentTypeDocument struct {
	XMLName   xml.Name              `xml:"Types"`
	Defaults  []contentTypeDefault  `xml:"Default"`
	Overrides []contentTypeOverride `xml:"Override"`
}

type contentTypeDefault struct {
	Extension   string `xml:"Extension,attr"`
	ContentType string `xml:"ContentType,attr"`
}

type contentTypeOverride struct {
	PartName    string `xml:"PartName,attr"`
	ContentType string `xml:"ContentType,attr"`
}

type packageContentTypes struct {
	defaults  map[string]string
	overrides map[string]string
}

func parseContentTypes(data []byte) (packageContentTypes, error) {
	var document contentTypeDocument
	if err := secureDecodeXML(data, &document); err != nil || document.XMLName.Local != "Types" || document.XMLName.Space != contentTypesNamespace {
		return packageContentTypes{}, errors.New("xlsx content types XML is invalid")
	}
	result := packageContentTypes{defaults: make(map[string]string), overrides: make(map[string]string)}
	for _, item := range document.Defaults {
		extension := strings.ToLower(strings.TrimSpace(item.Extension))
		contentType := strings.TrimSpace(item.ContentType)
		if extension == "" || strings.Contains(extension, ".") || contentType == "" {
			return packageContentTypes{}, errors.New("xlsx content types XML is invalid")
		}
		if _, exists := result.defaults[extension]; exists {
			return packageContentTypes{}, errors.New("xlsx content types contain duplicates")
		}
		result.defaults[extension] = contentType
	}
	for _, item := range document.Overrides {
		partName, err := normalizePartName(item.PartName)
		if err != nil || item.ContentType == "" {
			return packageContentTypes{}, errors.New("xlsx content types XML is invalid")
		}
		if _, exists := result.overrides[partName]; exists {
			return packageContentTypes{}, errors.New("xlsx content types contain duplicates")
		}
		result.overrides[partName] = strings.TrimSpace(item.ContentType)
	}
	return result, nil
}

func (types packageContentTypes) hasType(partName, expected string) bool {
	if actual, exists := types.overrides[partName]; exists {
		return actual == expected
	}
	extension := strings.ToLower(strings.TrimPrefix(path.Ext(partName), "."))
	return types.defaults[extension] == expected
}

func validatePackageTypes(types packageContentTypes, entries map[string]*zip.File) error {
	if !types.hasType(workbookPath, xlsxWorkbookType) {
		return errors.New("workbook is not a macro-free xlsx OOXML package")
	}
	for partName, contentType := range types.overrides {
		lowerType := strings.ToLower(contentType)
		lowerPart := strings.ToLower(partName)
		if strings.Contains(lowerType, "macro") || strings.Contains(lowerType, "vbaproject") || strings.Contains(lowerType, "externallink") || forbiddenPart(lowerPart) {
			return errors.New("macro-enabled or externally linked workbooks are not supported")
		}
		if _, exists := entries[partName]; !exists {
			return errors.New("xlsx content types reference a missing part")
		}
	}
	for _, contentType := range types.defaults {
		lowerType := strings.ToLower(contentType)
		if strings.Contains(lowerType, "macro") || strings.Contains(lowerType, "vbaproject") || strings.Contains(lowerType, "externallink") {
			return errors.New("macro-enabled or externally linked workbooks are not supported")
		}
	}
	return nil
}

func normalizePartName(value string) (string, error) {
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", errors.New("invalid OOXML part name")
	}
	name := strings.TrimPrefix(value, "/")
	if !safeEntryName(name) || strings.HasSuffix(name, "/") {
		return "", errors.New("invalid OOXML part name")
	}
	return name, nil
}

type workbookDocument struct {
	XMLName xml.Name        `xml:"workbook"`
	Sheets  []workbookSheet `xml:"sheets>sheet"`
}

type workbookSheet struct {
	State string
	RelID string
}

func (sheet *workbookSheet) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	for _, attribute := range start.Attr {
		switch attribute.Name.Local {
		case "state":
			sheet.State = strings.TrimSpace(attribute.Value)
		case "id":
			sheet.RelID = strings.TrimSpace(attribute.Value)
		}
	}
	return decoder.Skip()
}

func parseWorkbook(data []byte) (workbookDocument, error) {
	var document workbookDocument
	if err := secureDecodeXML(data, &document); err != nil || document.XMLName.Local != "workbook" || document.XMLName.Space != spreadsheetNamespace {
		return workbookDocument{}, errors.New("xlsx workbook XML is invalid")
	}
	return document, nil
}

func firstVisibleSheet(workbook workbookDocument) (string, error) {
	for _, sheet := range workbook.Sheets {
		switch sheet.State {
		case "", "visible":
			if sheet.RelID == "" {
				return "", errors.New("visible worksheet relationship is missing")
			}
			return sheet.RelID, nil
		case "hidden", "veryHidden":
		default:
			return "", errors.New("worksheet visibility state is invalid")
		}
	}
	return "", errors.New("workbook has no visible worksheet")
}

type relationshipDocument struct {
	XMLName       xml.Name       `xml:"Relationships"`
	Relationships []relationship `xml:"Relationship"`
}

type relationship struct {
	ID         string `xml:"Id,attr"`
	Type       string `xml:"Type,attr"`
	Target     string `xml:"Target,attr"`
	TargetMode string `xml:"TargetMode,attr"`
}

func parseRelationships(data []byte) (relationshipDocument, error) {
	var document relationshipDocument
	if err := secureDecodeXML(data, &document); err != nil || document.XMLName.Local != "Relationships" || document.XMLName.Space != relationshipNamespace {
		return relationshipDocument{}, errors.New("xlsx workbook relationships XML is invalid")
	}
	return document, nil
}

func validateRootRelationships(document relationshipDocument) error {
	ids := make(map[string]struct{}, len(document.Relationships))
	foundWorkbook := false
	for _, relationship := range document.Relationships {
		if relationship.ID == "" || relationship.Type == "" || relationship.Target == "" {
			return errors.New("xlsx root relationship is invalid")
		}
		if _, exists := ids[relationship.ID]; exists {
			return errors.New("xlsx root relationships contain duplicate IDs")
		}
		ids[relationship.ID] = struct{}{}
		if relationship.TargetMode != "" && relationship.TargetMode != "Internal" {
			return errors.New("external package relationships are not supported")
		}
		if strings.HasSuffix(relationship.Type, "/officeDocument") {
			if foundWorkbook {
				return errors.New("xlsx package contains duplicate workbook relationships")
			}
			resolved, err := resolveRootTarget(relationship.Target)
			if err != nil || resolved != workbookPath {
				return errors.New("xlsx workbook package relationship is invalid")
			}
			foundWorkbook = true
		}
	}
	if !foundWorkbook {
		return errors.New("xlsx workbook package relationship is missing")
	}
	return nil
}

func resolveRootTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "/") {
		return normalizePartName(target)
	}
	if !safeEntryName(target) || strings.HasSuffix(target, "/") || strings.ContainsAny(target, "?#") {
		return "", errors.New("xlsx root relationship target is unsafe")
	}
	return target, nil
}

func resolveWorkbookRelationships(document relationshipDocument, visibleRelID string) (string, string, error) {
	ids := make(map[string]struct{}, len(document.Relationships))
	var worksheetPath string
	var sharedStringsPath string
	for _, relationship := range document.Relationships {
		if relationship.ID == "" || relationship.Type == "" || relationship.Target == "" {
			return "", "", errors.New("xlsx workbook relationship is invalid")
		}
		if _, exists := ids[relationship.ID]; exists {
			return "", "", errors.New("xlsx workbook relationships contain duplicate IDs")
		}
		ids[relationship.ID] = struct{}{}
		if relationship.TargetMode != "" && relationship.TargetMode != "Internal" {
			return "", "", errors.New("external workbook relationships are not supported")
		}
		lowerType := strings.ToLower(relationship.Type)
		if strings.Contains(lowerType, "externallink") {
			return "", "", errors.New("external workbook relationships are not supported")
		}
		resolved, err := resolveWorkbookTarget(relationship.Target)
		if err != nil {
			return "", "", err
		}
		switch {
		case strings.HasSuffix(relationship.Type, "/worksheet"):
			if relationship.ID == visibleRelID {
				worksheetPath = resolved
			}
		case strings.HasSuffix(relationship.Type, "/sharedStrings"):
			if sharedStringsPath != "" {
				return "", "", errors.New("xlsx workbook has duplicate shared string relationships")
			}
			sharedStringsPath = resolved
		}
	}
	if worksheetPath == "" {
		return "", "", errors.New("visible worksheet relationship is invalid")
	}
	return worksheetPath, sharedStringsPath, nil
}

func resolveWorkbookTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" || strings.Contains(target, "\\") || strings.ContainsAny(target, "?#") {
		return "", errors.New("xlsx relationship target is unsafe")
	}
	if strings.HasPrefix(target, "/") {
		return normalizePartName(target)
	}
	for _, segment := range strings.Split(target, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("xlsx relationship target is unsafe")
		}
	}
	resolved := path.Join(path.Dir(workbookPath), target)
	if !safeEntryName(resolved) {
		return "", errors.New("xlsx relationship target is unsafe")
	}
	return resolved, nil
}

func parseSharedStrings(data []byte, limits Limits) ([]string, error) {
	decoder := newSecureXMLDecoder(data)
	root, err := nextStart(decoder)
	if err != nil || root.Name.Local != "sst" || root.Name.Space != spreadsheetNamespace {
		return nil, errors.New("xlsx shared strings XML is invalid")
	}
	values := make([]string, 0)
	for {
		token, tokenErr := secureToken(decoder)
		if tokenErr != nil {
			if errors.Is(tokenErr, io.EOF) {
				break
			}
			return nil, errors.New("xlsx shared strings XML is invalid")
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == "si" {
				value, parseErr := parseTextContainer(decoder, typed)
				if parseErr != nil || utf8.RuneCountInString(value) > limits.MaxCellRunes {
					return nil, errors.New("xlsx shared string exceeds the configured limit")
				}
				values = append(values, value)
			} else if skipErr := skipElement(decoder, typed); skipErr != nil {
				return nil, errors.New("xlsx shared strings XML is invalid")
			}
		case xml.EndElement:
			if typed.Name.Local == root.Name.Local {
				if err := requireXMLEOF(decoder); err != nil {
					return nil, errors.New("xlsx shared strings XML is invalid")
				}
				return values, nil
			}
		}
	}
	return nil, errors.New("xlsx shared strings XML is invalid")
}

func parseWorksheet(data []byte, sharedStrings []string, limits Limits) ([][]Cell, error) {
	decoder := newSecureXMLDecoder(data)
	root, err := nextStart(decoder)
	if err != nil || root.Name.Local != "worksheet" || root.Name.Space != spreadsheetNamespace {
		return nil, errors.New("xlsx worksheet XML is invalid")
	}
	var rows [][]Cell
	seenSheetData := false
	for {
		token, tokenErr := secureToken(decoder)
		if tokenErr != nil {
			return nil, errors.New("xlsx worksheet XML is invalid")
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == "sheetData" {
				if seenSheetData {
					return nil, errors.New("xlsx worksheet contains duplicate sheet data")
				}
				seenSheetData = true
				rows, err = parseSheetData(decoder, typed, sharedStrings, limits)
				if err != nil {
					return nil, err
				}
				continue
			}
			if skipErr := skipElement(decoder, typed); skipErr != nil {
				return nil, errors.New("xlsx worksheet XML is invalid")
			}
		case xml.EndElement:
			if typed.Name.Local == root.Name.Local {
				if err := requireXMLEOF(decoder); err != nil {
					return nil, errors.New("xlsx worksheet XML is invalid")
				}
				if !seenSheetData {
					return [][]Cell{}, nil
				}
				return rows, nil
			}
		}
	}
}

func parseSheetData(decoder *xml.Decoder, start xml.StartElement, sharedStrings []string, limits Limits) ([][]Cell, error) {
	rows := make([][]Cell, 0)
	lastRow := 0
	for {
		token, err := secureToken(decoder)
		if err != nil {
			return nil, errors.New("xlsx worksheet XML is invalid")
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local != "row" {
				if skipErr := skipElement(decoder, typed); skipErr != nil {
					return nil, errors.New("xlsx worksheet XML is invalid")
				}
				continue
			}
			rowNumber, parseErr := rowIndex(typed, lastRow+1, limits.MaxRows)
			if parseErr != nil || rowNumber <= lastRow {
				return nil, errors.New("xlsx worksheet row coordinate is invalid")
			}
			row, parseErr := parseRow(decoder, typed, rowNumber, sharedStrings, limits)
			if parseErr != nil {
				return nil, parseErr
			}
			for len(rows) < rowNumber-1 {
				rows = append(rows, []Cell{})
			}
			rows = append(rows, row)
			lastRow = rowNumber
		case xml.EndElement:
			if typed.Name.Local == start.Name.Local {
				return rows, nil
			}
		}
	}
}

func rowIndex(start xml.StartElement, fallback, maximum int) (int, error) {
	value := attributeValue(start, "r")
	if value == "" {
		if fallback < 1 || fallback > maximum {
			return 0, errors.New("row coordinate exceeds the configured limit")
		}
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 31)
	if err != nil || parsed < 1 || parsed > uint64(maximum) {
		return 0, errors.New("row coordinate exceeds the configured limit")
	}
	return int(parsed), nil
}

func parseRow(decoder *xml.Decoder, start xml.StartElement, rowNumber int, sharedStrings []string, limits Limits) ([]Cell, error) {
	row := make([]Cell, 0)
	lastColumn := 0
	for {
		token, err := secureToken(decoder)
		if err != nil {
			return nil, errors.New("xlsx worksheet row XML is invalid")
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local != "c" {
				if skipErr := skipElement(decoder, typed); skipErr != nil {
					return nil, errors.New("xlsx worksheet row XML is invalid")
				}
				continue
			}
			column, parseErr := cellColumn(typed, rowNumber, lastColumn+1, limits.MaxColumns)
			if parseErr != nil || column <= lastColumn {
				return nil, errors.New("xlsx worksheet cell coordinate is invalid")
			}
			cell, parseErr := parseCell(decoder, typed, sharedStrings, limits)
			if parseErr != nil {
				return nil, parseErr
			}
			for len(row) < column-1 {
				row = append(row, Cell{})
			}
			row = append(row, cell)
			lastColumn = column
		case xml.EndElement:
			if typed.Name.Local == start.Name.Local {
				return row, nil
			}
		}
	}
}

func cellColumn(start xml.StartElement, expectedRow, fallback, maximum int) (int, error) {
	reference := attributeValue(start, "r")
	if reference == "" {
		if fallback < 1 || fallback > maximum {
			return 0, errors.New("cell coordinate exceeds the configured limit")
		}
		return fallback, nil
	}
	column, row, err := parseCellReference(reference)
	if err != nil || row != expectedRow || column > maximum {
		return 0, errors.New("cell coordinate exceeds the configured limit")
	}
	return column, nil
}

func parseCellReference(reference string) (int, int, error) {
	if reference == "" {
		return 0, 0, errors.New("empty cell reference")
	}
	index := 0
	column := 0
	for index < len(reference) {
		character := reference[index]
		if character >= 'a' && character <= 'z' {
			character -= 'a' - 'A'
		}
		if character < 'A' || character > 'Z' {
			break
		}
		if column > (math.MaxInt-int(character-'A'+1))/26 {
			return 0, 0, errors.New("cell reference overflows")
		}
		column = column*26 + int(character-'A'+1)
		index++
	}
	if column == 0 || index == len(reference) {
		return 0, 0, errors.New("cell reference is invalid")
	}
	row, err := strconv.ParseUint(reference[index:], 10, 31)
	if err != nil || row < 1 {
		return 0, 0, errors.New("cell reference is invalid")
	}
	return column, int(row), nil
}

func parseCell(decoder *xml.Decoder, start xml.StartElement, sharedStrings []string, limits Limits) (Cell, error) {
	cellType := attributeValue(start, "t")
	var rawValue string
	var inlineValue string
	formula := false
	for {
		token, err := secureToken(decoder)
		if err != nil {
			return Cell{}, errors.New("xlsx worksheet cell XML is invalid")
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "v":
				value, readErr := readElementText(decoder, typed)
				if readErr != nil {
					return Cell{}, errors.New("xlsx worksheet cell XML is invalid")
				}
				rawValue = value
			case "is":
				value, readErr := parseTextContainer(decoder, typed)
				if readErr != nil {
					return Cell{}, errors.New("xlsx worksheet cell XML is invalid")
				}
				inlineValue = value
			case "f":
				formula = true
				if skipErr := skipElement(decoder, typed); skipErr != nil {
					return Cell{}, errors.New("xlsx worksheet cell XML is invalid")
				}
			default:
				if skipErr := skipElement(decoder, typed); skipErr != nil {
					return Cell{}, errors.New("xlsx worksheet cell XML is invalid")
				}
			}
		case xml.EndElement:
			if typed.Name.Local == start.Name.Local {
				value, valueErr := resolveCellValue(cellType, rawValue, inlineValue, sharedStrings)
				if valueErr != nil {
					return Cell{}, valueErr
				}
				if utf8.RuneCountInString(value) > limits.MaxCellRunes {
					return Cell{}, errors.New("xlsx cell value exceeds the configured limit")
				}
				return Cell{Value: value, Formula: formula}, nil
			}
		}
	}
}

func resolveCellValue(cellType, rawValue, inlineValue string, sharedStrings []string) (string, error) {
	switch cellType {
	case "", "n", "str", "d", "e":
		return rawValue, nil
	case "inlineStr":
		return inlineValue, nil
	case "s":
		index, err := strconv.ParseUint(strings.TrimSpace(rawValue), 10, 31)
		if err != nil || index >= uint64(len(sharedStrings)) {
			return "", errors.New("xlsx shared string index is invalid")
		}
		return sharedStrings[index], nil
	case "b":
		switch strings.TrimSpace(rawValue) {
		case "0":
			return "FALSE", nil
		case "1":
			return "TRUE", nil
		default:
			return "", errors.New("xlsx boolean cell is invalid")
		}
	default:
		return "", errors.New("xlsx cell type is unsupported")
	}
}

func parseTextContainer(decoder *xml.Decoder, start xml.StartElement) (string, error) {
	var builder strings.Builder
	depth := 1
	ignoredDepth := 0
	for depth > 0 {
		token, err := secureToken(decoder)
		if err != nil {
			return "", err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			depth++
			if typed.Name.Local == "rPh" {
				ignoredDepth = depth
			}
			if typed.Name.Local == "t" && ignoredDepth == 0 {
				text, readErr := readElementText(decoder, typed)
				if readErr != nil {
					return "", readErr
				}
				builder.WriteString(text)
				depth--
			}
		case xml.EndElement:
			if ignoredDepth == depth {
				ignoredDepth = 0
			}
			depth--
		}
	}
	return builder.String(), nil
}

func readElementText(decoder *xml.Decoder, start xml.StartElement) (string, error) {
	var builder strings.Builder
	for {
		token, err := secureToken(decoder)
		if err != nil {
			return "", err
		}
		switch typed := token.(type) {
		case xml.CharData:
			builder.Write([]byte(typed))
		case xml.StartElement:
			return "", errors.New("nested text element is invalid")
		case xml.EndElement:
			if typed.Name.Local == start.Name.Local {
				return builder.String(), nil
			}
		}
	}
}

func attributeValue(start xml.StartElement, localName string) string {
	for _, attribute := range start.Attr {
		if attribute.Name.Local == localName {
			return strings.TrimSpace(attribute.Value)
		}
	}
	return ""
}

func secureDecodeXML(data []byte, target any) error {
	probe := newSecureXMLDecoder(data)
	for {
		_, err := secureToken(probe)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	decoder := newSecureXMLDecoder(data)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireXMLEOF(decoder)
}

func requireXMLEOF(decoder *xml.Decoder) error {
	for {
		token, err := secureToken(decoder)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch typed := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(typed)) != "" {
				return errors.New("XML has trailing content")
			}
		case xml.ProcInst, xml.Comment:
			continue
		default:
			return errors.New("XML has multiple roots")
		}
	}
}

func newSecureXMLDecoder(data []byte) *xml.Decoder {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	return decoder
}

func secureToken(decoder *xml.Decoder) (xml.Token, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if _, forbidden := token.(xml.Directive); forbidden {
		return nil, errors.New("XML directives and DTDs are not supported")
	}
	return token, nil
}

func nextStart(decoder *xml.Decoder) (xml.StartElement, error) {
	for {
		token, err := secureToken(decoder)
		if err != nil {
			return xml.StartElement{}, err
		}
		if start, ok := token.(xml.StartElement); ok {
			return start, nil
		}
	}
}

func skipElement(decoder *xml.Decoder, start xml.StartElement) error {
	depth := 1
	for depth > 0 {
		token, err := secureToken(decoder)
		if err != nil {
			return err
		}
		switch token.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

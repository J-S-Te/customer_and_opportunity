package reportsecurity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/report"
)

// ValidatePDF 执行有界结构及活动内容校验。其
// 证据明确标识 static-v1，不代表杀毒判定。
func ValidatePDF(ctx context.Context, raw []byte, d report.FileDescriptor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	if len(raw) == 0 || len(raw) > maxPDFBytes || int64(len(raw)) != d.Size || d.MIME != "application/pdf" || !strings.EqualFold(hex.EncodeToString(hash[:]), d.FileHash) || filepath.Base(d.FileName) != d.FileName || strings.ContainsAny(d.FileName, "\\\x00\r\n") || !strings.HasSuffix(strings.ToLower(d.FileName), ".pdf") || !bytes.HasPrefix(raw, []byte("%PDF-")) {
		return ErrInvalid
	}
	lower := bytes.ToLower(raw)
	for _, marker := range []string{"eicar-standard-antivirus-test-file", "<script", "/javascript", "/launch", "/embeddedfile", "/openaction", "/encrypt"} {
		if bytes.Contains(lower, []byte(marker)) {
			return ErrInvalid
		}
	}
	conf := &model.Configuration{Reader15: true, ValidationMode: model.ValidationStrict, DecodeAllStreams: true}
	conf.ValidationMode = model.ValidationStrict
	pdf, err := api.ReadContext(bytes.NewReader(raw), conf)
	if err != nil {
		return fmt.Errorf("%w: PDF parse", ErrInvalid)
	}
	if err := api.ValidateContext(pdf); err != nil {
		return fmt.Errorf("%w: PDF structure", ErrInvalid)
	}
	if pdf.PageCount < 1 || pdf.PageCount > 500 {
		return fmt.Errorf("%w: PDF page limit", ErrInvalid)
	}
	for _, entry := range pdf.Table {
		if entry != nil && hasActiveObject(entry.Object, 0) {
			return fmt.Errorf("%w: PDF active content", ErrInvalid)
		}
	}
	return ctx.Err()
}
func hasActiveObject(object types.Object, depth int) bool {
	if depth > 64 {
		return true
	}
	switch obj := object.(type) {
	case types.Dict:
		for key, value := range obj {
			switch key {
			case "JS", "JavaScript", "Launch", "OpenAction", "AA", "EmbeddedFiles", "EF", "XFA", "RichMedia", "AcroForm", "Encrypt":
				return true
			}
			if name, ok := value.(types.Name); ok {
				switch name.Value() {
				case "JavaScript", "Launch", "EmbeddedFile", "Filespec", "RichMedia":
					return true
				}
			}
			if hasActiveObject(value, depth+1) {
				return true
			}
		}
	case types.Array:
		for _, value := range obj {
			if hasActiveObject(value, depth+1) {
				return true
			}
		}
	case types.StreamDict:
		return hasActiveObject(obj.Dict, depth+1)
	case *types.StreamDict:
		if obj != nil {
			return hasActiveObject(obj.Dict, depth+1)
		}
	}
	return false
}

package customer

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
)

// CodeImportScanner 是导入文件的进程内安全边界，不依赖外部杀毒服务。
// 它拒绝伪造容器、路径穿越、超大展开体积、超高压缩比和可执行/脚本型归档成员；
// safexlsx.ParseWorkbook 随后继续执行表头、单元格、公式和行列上限校验。
type CodeImportScanner struct{}

func (CodeImportScanner) Scan(_ context.Context, file []byte) error {
	if len(file) == 0 || len(file) > importMaxFileBytes || !bytes.HasPrefix(file, []byte("PK\x03\x04")) {
		return ErrImportInvalidFile
	}
	r, err := zip.NewReader(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		return ErrImportInvalidFile
	}
	if len(r.File) == 0 || len(r.File) > 2048 {
		return ErrImportInvalidFile
	}
	var expanded uint64
	for _, entry := range r.File {
		name := filepath.ToSlash(strings.TrimSpace(entry.Name))
		if name == "" || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "../") || entry.Mode()&os.ModeSymlink != 0 {
			return ErrImportFileUnsafe
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".exe" || ext == ".dll" || ext == ".dylib" || ext == ".sh" || ext == ".bat" || ext == ".cmd" || ext == ".js" || ext == ".vbs" || ext == ".ps1" {
			return ErrImportFileUnsafe
		}
		if entry.UncompressedSize64 > importMaxFileBytes || expanded > importMaxFileBytes-entry.UncompressedSize64 {
			return ErrImportInvalidFile
		}
		expanded += entry.UncompressedSize64
	}
	if expanded > uint64(len(file))*100 {
		return ErrImportInvalidFile
	}
	return nil
}

var _ ImportFileScanner = CodeImportScanner{}

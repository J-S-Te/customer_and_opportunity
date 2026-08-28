package filing

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strings"
)

const localMaterialScannerVersion = "portal-material-static-v1"
const maxMaterialImagePixels = uint64(100_000_000)

// LocalMaterialScanner 对文件网关中的不可变对象执行本地静态安全校验。它不会伪造外部
// 杀毒结果；引用明确标记 static-v1，后续可在不改变 MaterialService 的前提下替换为
// ClamAV 或独立扫描服务。
type LocalMaterialScanner struct{ store MaterialObjectStore }

// NewLocalMaterialScanner 创建只读取已终结不可变对象的静态扫描器。
func NewLocalMaterialScanner(store MaterialObjectStore) *LocalMaterialScanner {
	return &LocalMaterialScanner{store: store}
}

// Available 返回底层材料对象是否可供扫描。
func (scanner *LocalMaterialScanner) Available() bool {
	return scanner != nil && scanner.store != nil && scanner.store.Available()
}

// Submit 校验对象版本、大小、摘要及格式安全特征，并生成可审计的本地扫描引用。
func (scanner *LocalMaterialScanner) Submit(ctx context.Context, _ string, key, version, digest string, size uint64, media string) (string, error) {
	reader, err := scanner.store.OpenVerified(ctx, key, version, digest, size)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, int64(size)+1))
	if err != nil || uint64(len(content)) != size {
		return "", ErrMaterialContentInvalid
	}
	status := MaterialClean
	if validateLocalMaterialContent(content, media) != nil {
		status = MaterialRejected
	}
	return localMaterialScannerVersion + ":" + strings.ToLower(status) + ":" + strings.ToLower(digest), nil
}

// ImmediateStatus 表示本地静态扫描已同步完成，服务可直接固化 CLEAN/REJECTED，
// 无需伪造异步回调。
func (*LocalMaterialScanner) ImmediateStatus(reference string) (string, bool) {
	parts := strings.Split(reference, ":")
	if len(parts) != 3 || parts[0] != localMaterialScannerVersion {
		return "", false
	}
	status := strings.ToUpper(parts[1])
	return status, status == MaterialClean || status == MaterialRejected
}

func validateLocalMaterialContent(content []byte, media string) error {
	lower := bytes.ToLower(content)
	if bytes.Contains(lower, []byte("x5o!p%@ap[4\\pzx54(p^)7cc)7}$eicar-standard-antivirus-test-file!$h+h*")) ||
		bytes.HasPrefix(content, []byte("MZ")) || bytes.HasPrefix(content, []byte("\x7fELF")) ||
		bytes.HasPrefix(lower, []byte("#!")) || bytes.Contains(lower, []byte("<script")) {
		return ErrMaterialContentInvalid
	}
	switch media {
	case "application/pdf":
		if !bytes.HasPrefix(content, []byte("%PDF-")) || !bytes.Contains(content[maxMaterialInt(0, len(content)-2048):], []byte("%%EOF")) {
			return ErrMaterialContentInvalid
		}
		for _, marker := range [][]byte{[]byte("/javascript"), []byte("/js"), []byte("/launch"), []byte("/embeddedfile"), []byte("/openaction"), []byte("/aa"), []byte("/encrypt")} {
			if bytes.Contains(lower, marker) {
				return ErrMaterialContentInvalid
			}
		}
	case "image/png", "image/jpeg":
		configuration, detected, err := image.DecodeConfig(bytes.NewReader(content))
		if err != nil || configuration.Width <= 0 || configuration.Height <= 0 || uint64(configuration.Width)*uint64(configuration.Height) > maxMaterialImagePixels {
			return ErrMaterialContentInvalid
		}
		if media == "image/png" && detected != "png" || media == "image/jpeg" && detected != "jpeg" {
			return ErrMaterialContentInvalid
		}
	default:
		return ErrMaterialContentInvalid
	}
	return nil
}

func maxMaterialInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

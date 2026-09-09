package opportunity

import (
	"crypto/sha256"
	"encoding/hex"
)

func sha256Hex(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// testAttachmentContent 返回一份结构合法的最小 PDF 内容及其摘要。
func testAttachmentContent() ([]byte, string, uint64, string) {
	content := []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\nsimple pdf body\n%%EOF")
	return content, sha256Hex(content), uint64(len(content)), "application/pdf"
}

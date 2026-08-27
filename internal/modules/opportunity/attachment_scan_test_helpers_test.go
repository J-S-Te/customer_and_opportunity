package opportunity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"
)

func newByteReader(content []byte) io.Reader { return bytes.NewReader(content) }

func sha256Hex(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// testAttachmentContent 返回一份结构合法的最小 PDF 内容及其摘要。
func testAttachmentContent() ([]byte, string, uint64, string) {
	content := []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\nsimple pdf body\n%%EOF")
	return content, sha256Hex(content), uint64(len(content)), "application/pdf"
}

// startInlineClamd 启动一个模拟 clamd INSTREAM 协议的 TCP 服务器并返回其地址。
func startInlineClamd(t *testing.T, verdict string) (stop func(), address string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
				command := make([]byte, 10)
				if _, readErr := connection.Read(command); readErr != nil {
					return
				}
				header := make([]byte, 4)
				for {
					if _, readErr := io.ReadFull(connection, header); readErr != nil {
						return
					}
					size := binary.BigEndian.Uint32(header)
					if size == 0 {
						break
					}
					if _, readErr := io.CopyN(io.Discard, connection, int64(size)); readErr != nil {
						return
					}
				}
				_, _ = connection.Write([]byte(verdict + "\x00"))
			}()
		}
	}()
	return func() { _ = listener.Close() }, listener.Addr().String()
}

var _ = context.Background

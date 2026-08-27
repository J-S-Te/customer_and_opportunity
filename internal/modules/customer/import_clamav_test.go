package customer

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/clamav"
)

func startClamdForImportTest(t *testing.T, verdict string) *clamav.Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
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
					if _, copyErr := io.CopyN(io.Discard, connection, int64(size)); copyErr != nil {
						return
					}
				}
				_, _ = connection.Write([]byte(verdict + "\x00"))
			}()
		}
	}()
	client, clientErr := clamav.NewClient("tcp", listener.Addr().String())
	if clientErr != nil {
		t.Fatalf("NewClient: %v", clientErr)
	}
	return client
}

func TestClamAVImportScannerClean(t *testing.T) {
	client := startClamdForImportTest(t, "stream: OK")
	scanner, err := NewClamAVImportScanner(client, 5*time.Second)
	if err != nil {
		t.Fatalf("NewClamAVImportScanner: %v", err)
	}
	// xlsx 是 ZIP 容器：PK 头 + 填充。
	file := append([]byte("PK\x03\x04"), make([]byte, 128)...)
	if err = scanner.Scan(context.Background(), file); err != nil {
		t.Fatalf("Scan: %v", err)
	}
}

func TestClamAVImportScannerInfected(t *testing.T) {
	client := startClamdForImportTest(t, "stream: EICAR-Test-File")
	scanner, _ := NewClamAVImportScanner(client, 5*time.Second)
	file := append([]byte("PK\x03\x04"), make([]byte, 128)...)
	err := scanner.Scan(context.Background(), file)
	if !errors.Is(err, ErrImportFileUnsafe) {
		t.Fatalf("expected ErrImportFileUnsafe, got %v", err)
	}
}

func TestClamAVImportScannerInvalidContainer(t *testing.T) {
	client := startClamdForImportTest(t, "stream: OK")
	scanner, _ := NewClamAVImportScanner(client, 5*time.Second)
	// 非 ZIP 容器在进入引擎前就被拒绝。
	if err := scanner.Scan(context.Background(), []byte("plain text, not a workbook")); err != ErrImportInvalidFile {
		t.Fatalf("expected ErrImportInvalidFile, got %v", err)
	}
	// 空文件与超限文件同样拒绝。
	if err := scanner.Scan(context.Background(), nil); err != ErrImportInvalidFile {
		t.Fatalf("expected ErrImportInvalidFile for empty file, got %v", err)
	}
	oversized := append([]byte("PK\x03\x04"), make([]byte, importMaxFileBytes)...)
	if err := scanner.Scan(context.Background(), oversized); err != ErrImportInvalidFile {
		t.Fatalf("expected ErrImportInvalidFile for oversized file, got %v", err)
	}
}

func TestClamAVImportScannerEngineUnavailable(t *testing.T) {
	// 指向不存在的端口：扫描未完成必须报依赖不可用，而不是安全。
	client, err := clamav.NewClient("tcp", "127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	scanner, _ := NewClamAVImportScanner(client, 2*time.Second)
	file := append([]byte("PK\x03\x04"), make([]byte, 64)...)
	if err = scanner.Scan(context.Background(), file); err != ErrImportScannerUnavailable {
		t.Fatalf("expected ErrImportScannerUnavailable, got %v", err)
	}
}

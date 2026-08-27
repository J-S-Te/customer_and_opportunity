package clamav

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
)

// fakeClamd 模拟 clamd INSTREAM 协议：接收分块流，返回固定判定。
type fakeClamd struct {
	listener net.Listener
	verdict  string
	scanned  chan []byte
}

func newFakeClamd(t *testing.T, verdict string) *fakeClamd {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	daemon := &fakeClamd{listener: listener, verdict: verdict, scanned: make(chan []byte, 4)}
	go daemon.serve(t)
	t.Cleanup(func() { _ = listener.Close() })
	return daemon
}

func (d *fakeClamd) serve(t *testing.T) {
	for {
		connection, err := d.listener.Accept()
		if err != nil {
			return
		}
		go d.handle(t, connection)
	}
}

func (d *fakeClamd) handle(t *testing.T, connection net.Conn) {
	defer connection.Close()
	command := make([]byte, 10)
	if _, err := connection.Read(command); err != nil {
		return
	}
	if strings.HasPrefix(string(command), "zPING") {
		_, _ = connection.Write([]byte("PONG\x00"))
		return
	}
	if !strings.HasPrefix(string(command), "zINSTREAM") {
		_, _ = connection.Write([]byte("UNKNOWN COMMAND\x00"))
		return
	}
	var payload []byte
	header := make([]byte, 4)
	for {
		if _, err := connection.Read(header); err != nil {
			return
		}
		size := binary.BigEndian.Uint32(header)
		if size == 0 {
			break
		}
		chunk := make([]byte, size)
		if _, err := connection.Read(chunk); err != nil {
			return
		}
		payload = append(payload, chunk...)
	}
	select {
	case d.scanned <- payload:
	default:
	}
	_, _ = connection.Write([]byte(d.verdict + "\x00"))
}

func (d *fakeClamd) address() string { return d.listener.Addr().String() }

func TestScanStreamClean(t *testing.T) {
	daemon := newFakeClamd(t, "stream: OK")
	client, err := NewClient("tcp", daemon.address())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	content := strings.Repeat("business-plan-content-", 500) // 超过单块大小，验证分块
	if err = client.ScanStream(context.Background(), strings.NewReader(content)); err != nil {
		t.Fatalf("ScanStream: %v", err)
	}
	select {
	case scanned := <-daemon.scanned:
		if string(scanned) != content {
			t.Fatalf("scanned payload mismatch: got %d bytes, want %d", len(scanned), len(content))
		}
	default:
		t.Fatal("clamd did not receive payload")
	}
}

func TestScanStreamInfected(t *testing.T) {
	daemon := newFakeClamd(t, "stream: Win.Test.EICAR.NOT-A-VIRUS")
	client, err := NewClient("tcp", daemon.address())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	err = client.ScanBytes(context.Background(), []byte("malicious"))
	if !errors.Is(err, ErrInfected) {
		t.Fatalf("expected ErrInfected, got %v", err)
	}
	if !strings.Contains(err.Error(), "Win.Test.EICAR.NOT-A-VIRUS") {
		t.Fatalf("infection name missing: %v", err)
	}
}

func TestScanStreamEngineError(t *testing.T) {
	// clamd 对超限等内容返回非 OK/感染判定；这不是安全结论而是依赖错误。
	daemon := newFakeClamd(t, "INSTREAM size limit exceeded")
	client, _ := NewClient("tcp", daemon.address())
	err := client.ScanBytes(context.Background(), []byte("x"))
	if err == nil || errors.Is(err, ErrInfected) {
		t.Fatalf("expected dependency error, got %v", err)
	}
}

func TestScanStreamConnectionRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close() // 立即关闭，确保端口连接被拒绝
	client, _ := NewClient("tcp", address)
	if err = client.ScanBytes(context.Background(), []byte("x")); err == nil {
		t.Fatal("expected connection error")
	}
}

func TestPing(t *testing.T) {
	daemon := newFakeClamd(t, "")
	client, _ := NewClient("tcp", daemon.address())
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestNewClientValidation(t *testing.T) {
	if _, err := NewClient("grpc", "x"); err == nil {
		t.Fatal("expected unsupported network error")
	}
	if _, err := NewClient("tcp", ""); err == nil {
		t.Fatal("expected missing address error")
	}
	if _, err := NewClient("unix", ""); err == nil {
		t.Fatal("expected missing unix path error")
	}
}

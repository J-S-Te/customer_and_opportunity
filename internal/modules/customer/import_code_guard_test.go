package customer

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"
)

func makeGuardZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestCodeImportScannerRejectsUnsafeArchiveMembers(t *testing.T) {
	for _, test := range []struct {
		name    string
		member  string
		wantErr error
	}{
		{name: "path traversal", member: "../payload.txt", wantErr: ErrImportFileUnsafe},
		{name: "executable", member: "payload.exe", wantErr: ErrImportFileUnsafe},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := (CodeImportScanner{}).Scan(context.Background(), makeGuardZip(t, test.member, []byte("payload")))
			if err != test.wantErr {
				t.Fatalf("Scan() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestCodeImportScannerRejectsMalformedAndNonArchiveInput(t *testing.T) {
	for _, input := range [][]byte{nil, []byte("not-an-xlsx")} {
		if err := (CodeImportScanner{}).Scan(context.Background(), input); err != ErrImportInvalidFile {
			t.Fatalf("Scan(%q) error = %v, want %v", input, err, ErrImportInvalidFile)
		}
	}
}

func TestCodeImportScannerAcceptsBoundedArchive(t *testing.T) {
	if err := (CodeImportScanner{}).Scan(context.Background(), makeGuardZip(t, "xl/workbook.xml", []byte("workbook"))); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
}

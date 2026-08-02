package integrationhttp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTLSOptionsRejectPartialOrClearTextMTLS(t *testing.T) {
	if err := (TLSOptions{ClientCertFile: "client.pem"}).Validate(); err == nil {
		t.Fatal("partial client identity was accepted")
	}
	if err := (TLSOptions{RequireMTLS: true}).Validate(); err == nil {
		t.Fatal("required mTLS without an identity was accepted")
	}
	options := TLSOptions{ClientCertFile: "client.pem", ClientKeyFile: "client-key.pem", RequireMTLS: true}
	if err := options.ValidateEndpoints("http://service.example/internal"); err == nil {
		t.Fatal("mTLS policy was accepted for a clear-text endpoint")
	}
}

func TestNewTransportUsesPrivateRootAndClientIdentity(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	directory := t.TempDir()
	rootPath := filepath.Join(directory, "root.pem")
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(rootPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := writeSelfSignedClientIdentity(t, directory)
	transport, err := NewTransport(TLSOptions{
		RootCAFile: rootPath, ClientCertFile: certPath, ClientKeyFile: keyPath, RequireMTLS: true,
	}, time.Second)
	if err != nil {
		t.Fatalf("NewTransport() error = %v", err)
	}
	if transport.TLSClientConfig.MinVersion != 0x0303 || len(transport.TLSClientConfig.Certificates) != 1 || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("unexpected TLS policy: %#v", transport.TLSClientConfig)
	}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("private-root request failed: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func writeSelfSignedClientIdentity(t *testing.T, directory string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "integration-test-client"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(directory, "client.pem"), filepath.Join(directory, "client-key.pem")
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

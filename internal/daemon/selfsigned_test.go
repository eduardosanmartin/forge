package daemon

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureSelfSignedCertGeneratesValidLoadablePair(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, err := EnsureSelfSignedCert(dir)
	if err != nil {
		t.Fatalf("EnsureSelfSignedCert: %v", err)
	}

	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("generated pair does not load as a valid TLS certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse generated certificate: %v", err)
	}
	if cert.NotAfter.Before(time.Now().AddDate(1, 0, 0)) {
		t.Errorf("certificate expires too soon: %v", cert.NotAfter)
	}
	found := false
	for _, name := range cert.DNSNames {
		if name == "localhost" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected DNSNames to include localhost, got %v", cert.DNSNames)
	}
}

func TestEnsureSelfSignedCertReusesExistingPair(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, err := EnsureSelfSignedCert(dir)
	if err != nil {
		t.Fatalf("first EnsureSelfSignedCert: %v", err)
	}
	firstCert, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}

	certFile2, keyFile2, err := EnsureSelfSignedCert(dir)
	if err != nil {
		t.Fatalf("second EnsureSelfSignedCert: %v", err)
	}
	if certFile2 != certFile || keyFile2 != keyFile {
		t.Fatalf("expected identical paths across calls, got (%s,%s) then (%s,%s)", certFile, keyFile, certFile2, keyFile2)
	}
	secondCert, err := os.ReadFile(certFile2)
	if err != nil {
		t.Fatalf("read cert (second): %v", err)
	}
	if string(firstCert) != string(secondCert) {
		t.Error("a pre-existing cert/key pair should be reused, not regenerated, across restarts")
	}
}

func TestEnsureSelfSignedCertCreatesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "forge")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s should not exist yet", dir)
	}
	certFile, keyFile, err := EnsureSelfSignedCert(dir)
	if err != nil {
		t.Fatalf("EnsureSelfSignedCert: %v", err)
	}
	if _, err := os.Stat(certFile); err != nil {
		t.Errorf("cert file not created: %v", err)
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Errorf("key file not created: %v", err)
	}
}

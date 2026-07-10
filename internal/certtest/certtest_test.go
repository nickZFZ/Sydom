package certtest_test

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"

	"github.com/nickZFZ/Sydom/internal/certtest"
)

func TestLeafChainsToCA(t *testing.T) {
	ca := certtest.NewCA(t)
	certFile, keyFile := ca.Leaf(t, "leaf", x509.ExtKeyUsageServerAuth)
	if keyFile == "" {
		t.Fatal("want key file path")
	}

	caPEM, err := os.ReadFile(ca.File())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA PEM 无效")
	}

	leafPEM, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(leafPEM)
	if block == nil {
		t.Fatal("leaf PEM 无效")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("leaf 应链到 CA: %v", err)
	}
}

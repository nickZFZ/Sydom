package tlsconfig_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/tlsconfig"
)

// writeSelfSigned 生成自签证书写入 tmp，返回 certFile, keyFile（CA=该证书自身）。
func writeSelfSigned(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestServerNeitherSetReturnsNil(t *testing.T) {
	cfg, err := tlsconfig.Server("", "")
	if err != nil || cfg != nil {
		t.Fatalf("want (nil,nil), got (%v,%v)", cfg, err)
	}
}

func TestServerPartialConfigFailsClose(t *testing.T) {
	if _, err := tlsconfig.Server("only-cert.pem", ""); err == nil {
		t.Fatal("want error for partial config, got nil (silent plaintext is forbidden)")
	}
	if _, err := tlsconfig.Server("", "only-key.pem"); err == nil {
		t.Fatal("want error for partial config, got nil")
	}
}

func TestServerUnreadableCertFailsClose(t *testing.T) {
	if _, err := tlsconfig.Server("/no/such/cert.pem", "/no/such/key.pem"); err == nil {
		t.Fatal("want error for unreadable cert, got nil")
	}
}

func TestRoundTripTLSAndPlaintextRejected(t *testing.T) {
	certFile, keyFile := writeSelfSigned(t)
	srvCfg, err := tlsconfig.Server(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_ = c.(*tls.Conn).Handshake()
				c.Close()
			}(c)
		}
	}()
	// 带 CA 的客户端握手成功。
	cliCfg, err := tlsconfig.Client(certFile)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tls.Dial("tcp", ln.Addr().String(), cliCfg)
	if err != nil {
		t.Fatalf("TLS dial with CA should succeed: %v", err)
	}
	conn.Close()
	// 明文拨号 TLS 端口：服务端 TLS 握手失败并关闭连接，客户端绝不应读到有效应用响应（证明非静默降级）。
	raw, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = raw.Write([]byte("PLAINTEXT\n"))
	buf := make([]byte, 1)
	if n, rerr := raw.Read(buf); rerr == nil && n > 0 {
		t.Fatal("plaintext peer should not get a valid app response from TLS listener")
	}
}

func TestClientBadCAFailsClose(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("not a cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tlsconfig.Client(bad); err == nil {
		t.Fatal("want error for invalid CA pem, got nil")
	}
}

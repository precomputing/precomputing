package main

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
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// selfSigned writes a certificate for 127.0.0.1 with the given serial number, and its key.
func selfSigned(t *testing.T, serial int64) (certPEM, keyPEM []byte, cert *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "precomputing test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ = x509.ParseCertificate(der)
	kd, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), cert
}

func TestTLS(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	c1, k1, x1 := selfSigned(t, 1)
	c2, k2, x2 := selfSigned(t, 2)
	os.WriteFile(certPath, c1, 0o600)
	os.WriteFile(keyPath, k1, 0o600)

	if _, err := newCertFiles(certPath, filepath.Join(dir, "missing.pem")); err == nil {
		t.Error("a missing key was accepted")
	}
	os.WriteFile(filepath.Join(dir, "bad.pem"), []byte("not a key"), 0o600)
	if _, err := newCertFiles(certPath, filepath.Join(dir, "bad.pem")); err == nil {
		t.Error("a key that isn't one was accepted")
	}
	certs, err := newCertFiles(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }),
		TLSConfig: certs.tlsConfig()}
	go srv.ServeTLS(ln, "", "")
	defer srv.Close()
	url := "https://" + ln.Addr().String() + "/"

	pool := x509.NewCertPool()
	pool.AddCert(x1)
	pool.AddCert(x2)
	served := func(cfg *tls.Config) (int64, error) {
		tr := &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}
		defer tr.CloseIdleConnections()
		resp, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Get(url)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.TLS.PeerCertificates[0].SerialNumber.Int64(), nil
	}
	if n, err := served(&tls.Config{RootCAs: pool}); err != nil || n != 1 {
		t.Fatalf("first certificate: serial %d, %v", n, err)
	}
	// Renewed in place: the next connection gets the new pair, with no restart.
	os.WriteFile(certPath, c2, 0o600)
	os.WriteFile(keyPath, k2, 0o600)
	if n, err := served(&tls.Config{RootCAs: pool}); err != nil || n != 2 {
		t.Fatalf("after renewal: serial %d, %v", n, err)
	}
	// Half written, the certificate new and the key old: the last good pair stays in use.
	os.WriteFile(certPath, c1, 0o600)
	if n, err := served(&tls.Config{RootCAs: pool}); err != nil || n != 2 {
		t.Fatalf("with a mismatched pair: serial %d, %v; want the last good one", n, err)
	}
	os.WriteFile(keyPath, k1, 0o600)
	if n, err := served(&tls.Config{RootCAs: pool}); err != nil || n != 1 {
		t.Fatalf("once both files are in place: serial %d, %v", n, err)
	}
	// Old protocol versions and plain HTTP are turned away.
	if _, err := served(&tls.Config{RootCAs: pool, MaxVersion: tls.VersionTLS11}); err == nil {
		t.Error("a TLS 1.1 client was served")
	}
	if resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + ln.Addr().String() + "/"); err == nil {
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("plain HTTP to the TLS port: %d", resp.StatusCode)
		}
		resp.Body.Close()
	}
}

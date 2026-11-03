package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"sync"
)

// certFiles serves a certificate and key from files and reads them again when either file
// changes, so a renewed certificate is used from the next connection without a restart. A pair
// that fails to load is logged and the last good one stays in use.
type certFiles struct {
	certPath, keyPath string

	mu         sync.Mutex
	cert       *tls.Certificate
	certSeen   []byte
	keySeen    []byte
	lastFailed string
}

func newCertFiles(certPath, keyPath string) (*certFiles, error) {
	c := &certFiles{certPath: certPath, keyPath: keyPath}
	if _, err := c.get(); err != nil {
		return nil, err
	}
	return c, nil
}

// get returns the current pair, reading the files when their contents differ from the last read.
// Comparing contents, not times, catches a certificate replaced within the same second.
func (c *certFiles) get() (*tls.Certificate, error) {
	certPEM, err1 := os.ReadFile(c.certPath)
	keyPEM, err2 := os.ReadFile(c.keyPath)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err1 == nil && err2 == nil && c.cert != nil && bytes.Equal(certPEM, c.certSeen) && bytes.Equal(keyPEM, c.keySeen) {
		return c.cert, nil
	}
	var err error
	switch {
	case err1 != nil:
		err = err1
	case err2 != nil:
		err = err2
	default:
		var pair tls.Certificate
		if pair, err = tls.X509KeyPair(certPEM, keyPEM); err == nil {
			c.cert, c.certSeen, c.keySeen, c.lastFailed = &pair, certPEM, keyPEM, ""
			return c.cert, nil
		}
		err = fmt.Errorf("%s and %s: %v", c.certPath, c.keyPath, err)
	}
	if c.cert == nil {
		return nil, err
	}
	// Keep serving with the pair that worked; say once what went wrong with the new one. A pair
	// half written during a renewal fails here until both files are in place.
	if msg := err.Error(); msg != c.lastFailed {
		c.lastFailed = msg
		log.Printf("precomputing: keeping the certificate in use: %v", err)
	}
	return c.cert, nil
}

// tlsConfig is the server's TLS: version 1.2 or later, the certificate read from its files.
func (c *certFiles) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return c.get()
		},
	}
}

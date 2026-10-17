package security

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// CertReloader serves a certificate/key pair from disk and transparently
// reloads it when the files change, so certificates rotated by cert-manager,
// Vault or a cron job are picked up without restarting the node.
type CertReloader struct {
	certFile, keyFile string

	mu      sync.RWMutex
	cert    *tls.Certificate
	modTime time.Time
	checked time.Time
}

// reloadCheckInterval bounds how often the files are stat'ed.
const reloadCheckInterval = 10 * time.Second

// NewCertReloader loads the pair once and fails fast if it is invalid.
func NewCertReloader(certFile, keyFile string) (*CertReloader, error) {
	r := &CertReloader{certFile: certFile, keyFile: keyFile}
	if err := r.reload(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *CertReloader) reload() error {
	st, err := os.Stat(r.certFile)
	if err != nil {
		return fmt.Errorf("security: stat cert: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("security: load key pair: %w", err)
	}
	r.mu.Lock()
	r.cert, r.modTime, r.checked = &cert, st.ModTime(), time.Now()
	r.mu.Unlock()
	return nil
}

// GetCertificate implements tls.Config.GetCertificate.
func (r *CertReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.RLock()
	cert, modTime, checked := r.cert, r.modTime, r.checked
	r.mu.RUnlock()

	if time.Since(checked) > reloadCheckInterval {
		if st, err := os.Stat(r.certFile); err == nil && st.ModTime().After(modTime) {
			if err := r.reload(); err == nil {
				r.mu.RLock()
				cert = r.cert
				r.mu.RUnlock()
			}
			// On a failed reload keep serving the last good certificate.
		} else {
			r.mu.Lock()
			r.checked = time.Now()
			r.mu.Unlock()
		}
	}
	return cert, nil
}

// ServerTLSConfig returns a hardened TLS 1.3 server configuration. When
// caFile is set and requireClientCert is true, clients must present a
// certificate signed by that CA (mutual TLS).
func ServerTLSConfig(certFile, keyFile, caFile string, requireClientCert bool) (*tls.Config, error) {
	reloader, err := NewCertReloader(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: reloader.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}
	if requireClientCert {
		pool, err := loadCAPool(caFile)
		if err != nil {
			return nil, err
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

// ClientTLSConfig returns a TLS 1.3 client configuration that trusts caFile
// (system roots when empty) and optionally presents a client certificate.
func ClientTLSConfig(caFile, certFile, keyFile string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if caFile != "" {
		pool, err := loadCAPool(caFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	if certFile != "" || keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("security: load client key pair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

func loadCAPool(caFile string) (*x509.CertPool, error) {
	if caFile == "" {
		return nil, errors.New("security: CA file required")
	}
	pem, err := os.ReadFile(caFile) //nolint:gosec // path comes from operator configuration
	if err != nil {
		return nil, fmt.Errorf("security: read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("security: CA file contains no valid PEM certificates")
	}
	return pool, nil
}

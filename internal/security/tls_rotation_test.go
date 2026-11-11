package security

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerTLSCertificateRotationPreservesExistingStream(t *testing.T) {
	dir := t.TempDir()
	serverCAPEM, serverCAKey, serverCA := newTestCA(t, "Atlas server CA")
	clientCAPEM, clientCAKey, clientCA := newTestCA(t, "Atlas client CA")
	_, untrustedCAKey, untrustedCA := newTestCA(t, "Untrusted client CA")

	serverCertPath := filepath.Join(dir, "server.crt")
	serverKeyPath := filepath.Join(dir, "server.key")
	clientCertPath := filepath.Join(dir, "client.crt")
	clientKeyPath := filepath.Join(dir, "client.key")
	untrustedCertPath := filepath.Join(dir, "untrusted.crt")
	untrustedKeyPath := filepath.Join(dir, "untrusted.key")
	clientCAPath := filepath.Join(dir, "client-ca.crt")

	oldCertPEM, oldKeyPEM, oldSerial := newTestLeaf(t, serverCA, serverCAKey, "atlas.local", x509.ExtKeyUsageServerAuth, 1)
	writeTestPair(t, serverCertPath, serverKeyPath, oldCertPEM, oldKeyPEM)
	clientCertPEM, clientKeyPEM, _ := newTestLeaf(t, clientCA, clientCAKey, "atlas-client", x509.ExtKeyUsageClientAuth, 2)
	writeTestPair(t, clientCertPath, clientKeyPath, clientCertPEM, clientKeyPEM)
	untrustedCertPEM, untrustedKeyPEM, _ := newTestLeaf(t, untrustedCA, untrustedCAKey, "untrusted-client", x509.ExtKeyUsageClientAuth, 3)
	writeTestPair(t, untrustedCertPath, untrustedKeyPath, untrustedCertPEM, untrustedKeyPEM)
	if err := os.WriteFile(clientCAPath, clientCAPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	serverTLS, err := ServerTLSConfig(serverCertPath, serverKeyPath, clientCAPath, true)
	if err != nil {
		t.Fatal(err)
	}
	serverTLS.NextProtos = []string{"http/1.1"}
	releaseStream := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/watch" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()
		select {
		case <-releaseStream:
			_, _ = fmt.Fprint(w, "data: still-connected\n\n")
			flusher.Flush()
		case <-r.Context().Done():
		}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, TLSConfig: serverTLS}
	go func() {
		if err := server.ServeTLS(listener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("TLS test server: %v", err)
		}
	}()
	defer server.Close()
	serverURL := "https://" + listener.Addr().String()

	newClient := func(certPath, keyPath string) *http.Client {
		t.Helper()
		clientTLS, err := ClientTLSConfig(writeTestCA(t, dir, "server-ca.crt", serverCAPEM), certPath, keyPath)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}}
	}
	client := newClient(clientCertPath, clientKeyPath)
	defer client.Transport.(*http.Transport).CloseIdleConnections()

	streamContext, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	streamRequest, err := http.NewRequestWithContext(streamContext, http.MethodGet, serverURL+"/watch", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamResponse, err := client.Do(streamRequest)
	if err != nil {
		t.Fatalf("open mTLS watch stream: %v", err)
	}
	defer streamResponse.Body.Close()
	if streamResponse.StatusCode != http.StatusOK {
		t.Fatalf("watch status = %d, want 200", streamResponse.StatusCode)
	}
	streamReader := bufio.NewReader(streamResponse.Body)
	if line, err := streamReader.ReadString('\n'); err != nil || line != ": connected\n" {
		t.Fatalf("initial stream frame = %q, %v", line, err)
	}
	if line, err := streamReader.ReadString('\n'); err != nil || line != "\n" {
		t.Fatalf("initial stream separator = %q, %v", line, err)
	}

	newCertPEM, newKeyPEM, newSerial := newTestLeaf(t, serverCA, serverCAKey, "atlas.local", x509.ExtKeyUsageServerAuth, 4)
	if err := os.WriteFile(serverKeyPath, newKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serverCertPath, newCertPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(serverCertPath, future, future); err != nil {
		t.Fatal(err)
	}

	// Match the reloader's public polling behavior rather than reaching into its
	// state; the long-lived stream remains open while the next handshake reloads.
	time.Sleep(reloadCheckInterval + 100*time.Millisecond)
	rotatedClient := newClient(clientCertPath, clientKeyPath)
	rotatedResponse, err := rotatedClient.Get(serverURL + "/")
	if err != nil {
		t.Fatalf("new connection after certificate rotation: %v", err)
	}
	rotatedResponse.Body.Close()
	rotatedClient.Transport.(*http.Transport).CloseIdleConnections()
	if rotatedResponse.TLS == nil || len(rotatedResponse.TLS.PeerCertificates) == 0 || rotatedResponse.TLS.PeerCertificates[0].SerialNumber.Cmp(newSerial) != 0 {
		got := "no peer certificate"
		if rotatedResponse.TLS != nil && len(rotatedResponse.TLS.PeerCertificates) > 0 {
			got = rotatedResponse.TLS.PeerCertificates[0].SerialNumber.String()
		}
		t.Fatalf("new connection served certificate serial %s, want %s", got, newSerial)
	}
	if oldSerial.Cmp(newSerial) == 0 {
		t.Fatal("test certificates unexpectedly have the same serial")
	}

	untrustedClient := newClient(untrustedCertPath, untrustedKeyPath)
	untrustedResponse, err := untrustedClient.Get(serverURL + "/")
	untrustedClient.Transport.(*http.Transport).CloseIdleConnections()
	if err == nil {
		_ = untrustedResponse.Body.Close()
		t.Fatal("client certificate from an unknown CA was accepted")
	}

	close(releaseStream)
	if line, err := streamReader.ReadString('\n'); err != nil || line != "data: still-connected\n" {
		t.Fatalf("existing watch stream after rotation = %q, %v", line, err)
	}
}

func newTestCA(t *testing.T, commonName string) ([]byte, *ecdsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key, cert
}

func newTestLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string, usage x509.ExtKeyUsage, serial int64) ([]byte, []byte, *big.Int) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		template.SerialNumber
}

func writeTestPair(t *testing.T, certPath, keyPath string, certPEM, keyPEM []byte) {
	t.Helper()
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTestCA(t *testing.T, dir, name string, certPEM []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

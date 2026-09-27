package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"time"

	"glocker/internal/config"
)

// StartWebTrackingServer starts HTTP and HTTPS servers for web tracking and browser extension communication.
// The HTTP server runs on port 80 and HTTPS on port 443 with a self-signed certificate.
func StartWebTrackingServer(cfg *config.Config) {
	slog.Debug("Starting web tracking servers on ports 80 and 443")

	// Setup routes
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		HandleWebTrackingRequest(cfg, w, r)
	})

	http.HandleFunc("/report", func(w http.ResponseWriter, r *http.Request) {
		HandleReportRequest(cfg, w, r)
	})

	http.HandleFunc("/keywords", func(w http.ResponseWriter, r *http.Request) {
		HandleKeywordsRequest(cfg, w, r)
	})

	http.HandleFunc("/keywords-stream", func(w http.ResponseWriter, r *http.Request) {
		HandleSSERequest(cfg, w, r)
	})

	http.HandleFunc("/blocked", func(w http.ResponseWriter, r *http.Request) {
		HandleBlockedPageRequest(w, r)
	})

	// The /stats dashboard is now served by the standalone glockpeek process
	// (cmd/glockpeek), not the daemon. The daemon's web server only handles
	// browser-extension traffic here.

	// Bind loopback only (both v4 AND v6): the extension uses 127.0.0.1, and
	// hosts-blocked domains resolve to BOTH 127.0.0.1 and ::1 (see
	// enforcement/hosts.go), so a client reaching the blocked page over ::1 must
	// still be served + recorded. But NOT all interfaces — ":80"/":443" exposed
	// /keywords, /report and the blocked-page command trigger to the whole LAN.
	for _, addr := range []string{"127.0.0.1:80", "[::1]:80"} {
		addr := addr
		go func() {
			log.Printf("Web tracking HTTP server started on %s", addr)
			if err := (&http.Server{Addr: addr}).ListenAndServe(); err != nil {
				log.Printf("Web tracking HTTP server (%s) error: %v", addr, err)
			}
		}()
	}

	// Generate the self-signed cert once (in memory), shared by both loopback
	// HTTPS listeners. Nothing is written to disk.
	cert, err := generateSelfSignedCert()
	if err != nil {
		log.Printf("Failed to generate SSL certificate: %v", err)
		return
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	for _, addr := range []string{"127.0.0.1:443", "[::1]:443"} {
		addr := addr
		go func() {
			log.Printf("Web tracking HTTPS server started on %s", addr)
			if err := (&http.Server{Addr: addr, TLSConfig: tlsCfg}).ListenAndServeTLS("", ""); err != nil {
				log.Printf("Web tracking HTTPS server (%s) error: %v", addr, err)
			}
		}()
	}
}

// generateSelfSignedCert creates a temporary self-signed SSL certificate for HTTPS.
// Returns paths to the certificate and key files.
// generateSelfSignedCert builds a self-signed loopback certificate entirely in
// memory. The private key never touches disk, so there's no fixed /tmp key file
// a local user could pre-create or read, and no dependency on the openssl binary.
func generateSelfSignedCert() (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("serial: %w", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}

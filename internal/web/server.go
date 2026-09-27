package web

import (
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"

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

	// Start HTTP server. Bind loopback only: the extension talks to 127.0.0.1 and
	// hosts-blocked domains resolve to 127.0.0.1, so nothing legitimate comes from
	// the LAN. Binding ":80" exposed /keywords, /report and the blocked-page
	// command trigger to every host on the network.
	go func() {
		server := &http.Server{
			Addr:    "127.0.0.1:80",
			Handler: nil,
		}

		log.Printf("Web tracking HTTP server started on port 80")
		if err := server.ListenAndServe(); err != nil {
			log.Printf("Web tracking HTTP server error: %v", err)
		}
	}()

	// Start HTTPS server (loopback only, same reasoning as :80 above).
	go func() {
		server := &http.Server{
			Addr:    "127.0.0.1:443",
			Handler: nil,
		}

		// Generate self-signed certificate
		certFile, keyFile, err := generateSelfSignedCert()
		if err != nil {
			log.Printf("Failed to generate SSL certificate: %v", err)
			return
		}
		defer os.Remove(certFile)
		defer os.Remove(keyFile)

		log.Printf("Web tracking HTTPS server started on port 443")
		if err := server.ListenAndServeTLS(certFile, keyFile); err != nil {
			log.Printf("Web tracking HTTPS server error: %v", err)
		}
	}()
}

// generateSelfSignedCert creates a temporary self-signed SSL certificate for HTTPS.
// Returns paths to the certificate and key files.
func generateSelfSignedCert() (string, string, error) {
	// Generate a private key
	priv, err := exec.Command("openssl", "genrsa", "-out", "/tmp/glocker-key.pem", "2048").CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("failed to generate private key: %v, output: %s", err, priv)
	}

	// Generate a self-signed certificate
	cert, err := exec.Command("openssl", "req", "-new", "-x509", "-key", "/tmp/glocker-key.pem",
		"-out", "/tmp/glocker-cert.pem", "-days", "365", "-subj", "/CN=localhost").CombinedOutput()
	if err != nil {
		os.Remove("/tmp/glocker-key.pem")
		return "", "", fmt.Errorf("failed to generate certificate: %v, output: %s", err, cert)
	}

	return "/tmp/glocker-cert.pem", "/tmp/glocker-key.pem", nil
}

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

	// Generate the self-signed cert once, shared by both loopback HTTPS listeners.
	certFile, keyFile, err := generateSelfSignedCert()
	if err != nil {
		log.Printf("Failed to generate SSL certificate: %v", err)
		return
	}
	// NB: not removed — both servers need the files for the process lifetime, and
	// the daemon runs until exit. (The fixed /tmp path is a separate item.)
	for _, addr := range []string{"127.0.0.1:443", "[::1]:443"} {
		addr := addr
		go func() {
			log.Printf("Web tracking HTTPS server started on %s", addr)
			if err := (&http.Server{Addr: addr}).ListenAndServeTLS(certFile, keyFile); err != nil {
				log.Printf("Web tracking HTTPS server (%s) error: %v", addr, err)
			}
		}()
	}
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

package web

import (
	"fmt"
	"html"
	"net/http"
	"time"
)

// HandleBlockedPageRequest displays a blocked page to the user when they try to access a blocked domain.
func HandleBlockedPageRequest(w http.ResponseWriter, r *http.Request) {
	// Only accept GET requests
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Get parameters from the query string
	domain := r.URL.Query().Get("domain")
	matchedDomain := r.URL.Query().Get("matched")
	originalURL := r.URL.Query().Get("url")
	reason := r.URL.Query().Get("reason")

	// Set defaults if parameters are missing
	if domain == "" {
		domain = "this site"
	}
	if reason == "" {
		reason = "This content has been blocked by Glocker."
	}

	// charset so the 🚫 (and any non-ASCII) renders instead of mojibake.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// These are reflected from the query string into the page, so HTML-escape them
	// to prevent reflected XSS — any site can navigate the browser to
	// /blocked?reason=<script>… on this local origin.
	// Only show the matched-rule line when a real rule was passed (domain blocks).
	// Keyword blocks pass none, so it would just echo the "this site" fallback.
	matchedInfo := ""
	if matchedDomain != "" {
		matchedInfo = fmt.Sprintf(`<p class="matched">Matched blocking rule: %s</p>`, html.EscapeString(matchedDomain))
	}

	// Add original URL info if provided
	originalURLInfo := ""
	if originalURL != "" {
		originalURLInfo = fmt.Sprintf(`<p class="matched">Original URL: %s</p>`, html.EscapeString(originalURL))
	}

	// Generate the blocked page HTML
	blockedPage := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
    <title>Site Blocked - Glocker</title>
    <style>
        body {
            font-family: Arial, sans-serif;
            text-align: center;
            margin-top: 100px;
            background-color: #f0f0f0;
        }
        .container {
            max-width: 600px;
            margin: 0 auto;
            padding: 20px;
            background-color: white;
            border-radius: 10px;
            box-shadow: 0 4px 6px rgba(0,0,0,0.1);
        }
        h1 {
            color: #d32f2f;
        }
        p {
            color: #666;
            line-height: 1.6;
        }
        .domain {
            font-weight: bold;
            color: #1976d2;
            background-color: #e3f2fd;
            padding: 8px 12px;
            border-radius: 5px;
            margin: 10px 0;
            display: inline-block;
        }
        .matched {
            font-size: 0.9em;
            color: #888;
            margin: 10px 0;
        }
        .reason {
            font-size: 0.95em;
            color: #d32f2f;
            background-color: #ffebee;
            padding: 10px 15px;
            border-radius: 5px;
            margin: 15px 0;
            border-left: 4px solid #d32f2f;
        }
        .time {
            color: #888;
            font-size: 0.9em;
        }
        .command-hint {
            background-color: #f8f9fa;
            border-left: 4px solid #1976d2;
            padding: 15px;
            margin: 20px 0;
            text-align: left;
            border-radius: 4px;
        }
        .command {
            font-family: 'Courier New', monospace;
            background-color: #e9ecef;
            padding: 5px 8px;
            border-radius: 3px;
            font-size: 0.9em;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>🚫 Site Blocked</h1>
        <p>Access to <span class="domain">%s</span> has been blocked by Glocker.</p>
        %s
        %s
        <div class="reason">%s</div>
        <p class="time">Blocked at: %s</p>
    </div>
</body>
</html>`, html.EscapeString(domain), matchedInfo, originalURLInfo, html.EscapeString(reason), time.Now().Format("2006-01-02 15:04:05"))

	w.Write([]byte(blockedPage))
}

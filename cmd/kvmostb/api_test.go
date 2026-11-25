package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestVideoOfflineAPI verifies the no-capture fault mode: with a nil
// Capture the service must keep answering: /api/status reports
// offline, the stream/screenshot endpoints answer 503 (not panic),
// resolutions answers an empty menu.
func TestVideoOfflineAPI(t *testing.T) {
	rt := &Runtime{Capture: nil, HID: nil}

	w := httptest.NewRecorder()
	rt.handleStatus(w, httptest.NewRequest("GET", "/api/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"capturing":false`) {
		t.Errorf("status body missing capturing:false: %s", body)
	}
	if !strings.Contains(body, "video offline") {
		t.Errorf("status body missing offline stream_info: %s", body)
	}

	w = httptest.NewRecorder()
	rt.handleVideoStream(w, httptest.NewRequest("GET", "/api/video/stream", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("stream code = %d, want 503", w.Code)
	}

	w = httptest.NewRecorder()
	rt.handleScreenshot(w, httptest.NewRequest("GET", "/api/video/screenshot", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("screenshot code = %d, want 503", w.Code)
	}

	w = httptest.NewRecorder()
	rt.handleResolutions(w, httptest.NewRequest("GET", "/api/video/resolutions", nil))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("resolutions = %d %q, want 200 []", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	rt.handleSetResolution(w, httptest.NewRequest("POST", "/api/video/resolution?width=1280&height=720&fps=30", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("set resolution code = %d, want 503", w.Code)
	}
}

// TestListenDefault pins the -listen default: env LISTEN_ADDRESS /
// LISTEN_PORT override, sane defaults otherwise.
func TestListenDefault(t *testing.T) {
	t.Setenv("LISTEN_ADDRESS", "")
	t.Setenv("LISTEN_PORT", "")
	if got := listenDefault(); got != "0.0.0.0:8080" {
		t.Fatalf("default: got %q, want 0.0.0.0:8080", got)
	}
	t.Setenv("LISTEN_ADDRESS", "127.0.0.1")
	t.Setenv("LISTEN_PORT", "9090")
	if got := listenDefault(); got != "127.0.0.1:9090" {
		t.Fatalf("env override: got %q, want 127.0.0.1:9090", got)
	}
}

// TestPublicPathStaticAssets pins the public asset surface: the login
// page's icon/favicon requests must stay public, the console stays gated.
func TestPublicPathStaticAssets(t *testing.T) {
	for _, p := range []string{"/login", "/shared-css.css", "/api/login",
		"/kvmostb.webp", "/kvmostb.png", "/favicon.ico", "/favicon/favicon-32x32.png", "/favicon/site.webmanifest"} {
		if !publicPath(p) {
			t.Errorf("publicPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"/", "/index.html", "/app.js", "/api/status", "/shared-css.css.evil"} {
		if publicPath(p) {
			t.Errorf("publicPath(%q) = true, want false", p)
		}
	}
}

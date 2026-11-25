package main

/*
	UI smoke test: serves the embedded web console with stubbed API
	endpoints and exercises it over real HTTP, including the session
	cookie login/logout flow. Run a headless browser (or curl) against
	the returned address.

	Kept dependency-free: no capture device, no gadget required.
*/

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// stubMux builds the dev mux: stub API endpoints, the real session-auth
// endpoints (auth.go) and the embedded web console.
func stubMux() http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"video_device": "/dev/video1", "capturing": true,
			"stream_info": "test 1280x720 30fps", "width": 1280, "height": 720, "fps": 30,
			"bitrate_bps": 12345678, "udc_state": "configured", "iso_file": "",
			"hid_available": true, "audio_available": false,
		})
	})
	mux.HandleFunc("/api/video/resolutions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"width": 640, "height": 480, "fps": []int{30, 60}},
			{"width": 1280, "height": 720, "fps": []int{30, 60}},
			{"width": 1920, "height": 1080, "fps": []int{25, 30, 60}},
		})
	})
	mux.HandleFunc("/api/video/resolution", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/iso/list", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"name": "debian.iso", "path": "/var/lib/kvmostb/iso/debian.iso", "source": "local", "size": 1234, "mod_time": 1},
		})
	})
	mux.HandleFunc("/api/iso/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"iso_file": "", "udc_state": "configured"})
	})
	mux.HandleFunc("/api/iso/mount", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "iso_file": r.URL.Query().Get("file")})
	})
	mux.HandleFunc("/api/hid/jiggler", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"running": false})
	})
	mux.HandleFunc("/api/hid/reset", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/ws/hid", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "stub", http.StatusNotImplemented)
	})
	/* Session auth: the real handlers (login reads ADMIN_USER/
	   ADMIN_PASSWORD from the environment at request time). */
	mux.HandleFunc("/login", serveLoginPage)
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/api/logout", handleLogout)
	mux.Handle("/", webHandler())
	return mux
}

// TestWebConsole_Served verifies the embedded console assets and the
// session cookie auth flow end to end. Browser-level input and MJPEG are
// covered on real hardware.
func TestWebConsole_Served(t *testing.T) {
	t.Setenv("ADMIN_USER", "testuser")
	t.Setenv("ADMIN_PASSWORD", "testpass")

	srv := httptest.NewServer(noCacheAPI(healthWrap(sessionAuth(stubMux()))))
	t.Cleanup(srv.Close)

	// noRedirect stops the client from following the 302 to /login.
	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	do := func(client *http.Client, method, url, body string) *http.Response {
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	t.Run("no_cookie_html_302_login", func(t *testing.T) {
		res := do(noRedirect, "GET", srv.URL+"/", "")
		defer res.Body.Close()
		if res.StatusCode != http.StatusFound {
			t.Fatalf("GET / status = %d, want 302", res.StatusCode)
		}
		if loc := res.Header.Get("Location"); loc != "/login" {
			t.Fatalf("Location = %q, want /login", loc)
		}
	})

	t.Run("no_cookie_api_401", func(t *testing.T) {
		res := do(noRedirect, "GET", srv.URL+"/api/status", "")
		defer res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET /api/status status = %d, want 401", res.StatusCode)
		}
	})

	t.Run("login_page_public", func(t *testing.T) {
		res := do(noRedirect, "GET", srv.URL+"/login", "")
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /login status = %d, want 200", res.StatusCode)
		}
	})

	t.Run("login_wrong_creds_401", func(t *testing.T) {
		res := do(noRedirect, "POST", srv.URL+"/api/login",
			`{"username":"testuser","password":"wrongpass"}`)
		defer res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("login status = %d, want 401", res.StatusCode)
		}
	})

	// jar client: Set-Cookie values are replayed automatically.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	var sessionToken string
	t.Run("login_success_sets_cookie", func(t *testing.T) {
		res := do(client, "POST", srv.URL+"/api/login",
			`{"username":"testuser","password":"testpass"}`)
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("login status = %d, want 200", res.StatusCode)
		}
		sc := res.Header.Get("Set-Cookie")
		for _, want := range []string{"kvmostb_session=", "Path=/", "HttpOnly", "SameSite=Lax"} {
			if !strings.Contains(sc, want) {
				t.Errorf("Set-Cookie %q missing %q", sc, want)
			}
		}
		if strings.Contains(sc, "Secure") {
			t.Errorf("Set-Cookie must not be Secure (plain HTTP LAN): %q", sc)
		}
		// capture the raw token: "kvmostb_session=<token>"
		sessionToken = strings.TrimPrefix(strings.SplitN(sc, ";", 2)[0], "kvmostb_session=")
		if sessionToken == "" {
			t.Fatal("no session token in Set-Cookie")
		}
	})

	t.Run("authed_requests_ok", func(t *testing.T) {
		for _, path := range []string{"/", "/api/status", "/app.js", "/shared-css.css"} {
			res := do(client, "GET", srv.URL+path, "")
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200", path, res.StatusCode)
			}
		}
	})

	t.Run("logout_invalidates_cookie", func(t *testing.T) {
		res := do(client, "POST", srv.URL+"/api/logout", "")
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("logout status = %d, want 200", res.StatusCode)
		}
		// The old token must be rejected server-side (send it explicitly;
		// the jar already dropped it on the Max-Age=0 response).
		req, _ := http.NewRequest("GET", srv.URL+"/api/status", nil)
		req.Header.Set("Cookie", "kvmostb_session="+sessionToken)
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET /api/status after logout = %d, want 401", res.StatusCode)
		}
	})
}

// TestWebConsole_ServeStub starts the stub console on 127.0.0.1:8097 (no auth,
// local only) and blocks until /tmp/uistub-stop exists. It is opt-in so that
// `go test ./...` finishes:
//
//	KVMOSTB_UI_STUB=1 go test ./cmd/kvmostb/ -run TestWebConsole_ServeStub
func TestWebConsole_ServeStub(t *testing.T) {
	if os.Getenv("KVMOSTB_UI_STUB") == "" {
		t.Skip("set KVMOSTB_UI_STUB=1 to serve the stub console")
	}
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"video_device": "/dev/video1", "capturing": true,
			"stream_info": "USB Video: test MJPEG [1280x720] 30 fps | live 2.1 Mbps",
			"width":       1280, "height": 720, "fps": 30, "bitrate_bps": 2100000,
			"udc_state": "configured", "iso_file": "", "hid_available": true,
			"audio_available": false, "audio_note": "stub",
		})
	})
	mux.HandleFunc("/api/video/resolutions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"width": 640, "height": 480, "fps": []int{30, 60}},
			{"width": 1280, "height": 720, "fps": []int{30, 60}},
			{"width": 1920, "height": 1080, "fps": []int{25, 30, 60}},
		})
	})
	mux.HandleFunc("/api/video/resolution", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "stream_info": "changed (stub)"})
	})
	mux.HandleFunc("/api/iso/list", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"name": "debian-13.iso", "path": "/var/lib/kvmostb/iso/debian-13.iso", "source": "local", "size": 838860800, "mod_time": 1},
			{"name": "current.iso", "path": "/mnt/shared/iso/current.iso", "source": "nfs", "size": 419430400, "mod_time": 2},
		})
	})
	mux.HandleFunc("/api/iso/mount", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "iso_file": r.URL.Query().Get("file")})
	})
	mux.HandleFunc("/api/hid/jiggler", func(w http.ResponseWriter, r *http.Request) {
		running := r.URL.Query().Get("enable") == "1" && r.Method == http.MethodPost
		writeJSON(w, map[string]any{"running": running})
	})
	mux.HandleFunc("/api/hid/reset", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/ws/hid", func(w http.ResponseWriter, r *http.Request) {
		// Minimal RFC6455 handshake so the browser keeps the socket open;
		// frames are drained and discarded (the real backend consumes them).
		key := r.Header.Get("Sec-WebSocket-Key")
		sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		w.Header().Set("Upgrade", "websocket")
		w.Header().Set("Connection", "Upgrade")
		w.Header().Set("Sec-WebSocket-Accept", base64.StdEncoding.EncodeToString(sum[:]))
		w.WriteHeader(http.StatusSwitchingProtocols)
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		buf := make([]byte, 4096)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	})
	/* Session-auth stubs: the dev console is unauthenticated, these just
	   keep the login page working if visited (accept any credentials). */
	mux.HandleFunc("/login", serveLoginPage)
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/logout", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.Handle("/", webHandler())

	srv := &http.Server{Addr: "127.0.0.1:8097", Handler: noCacheAPI(mux)}
	go func() { _ = srv.ListenAndServe() }()
	t.Log("stub console on http://127.0.0.1:8097 (stop: touch /tmp/uistub-stop)")
	for {
		if _, err := os.Stat("/tmp/uistub-stop"); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

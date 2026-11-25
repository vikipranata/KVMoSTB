package main

/*
	HTTP routing and the JSON API the web UI drives.
*/

import (
	"encoding/json"
	"fmt"
	"kvmostb/internal/logging"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"kvmostb/internal/capture"
	"kvmostb/internal/gadget"
	"kvmostb/internal/hid"
	"kvmostb/internal/iso"
)

// Runtime holds the hardware handles the HTTP layer needs.
type Runtime struct {
	Capture *capture.Instance
	// HID is the USB gadget HID controller. nil = input disabled.
	HID *hid.Controller
	// Gadget is the configfs gadget (ISO hot-swap, UDC status).
	Gadget *gadget.Gadget
}

// resolutionMu serialises resolution changes: StopVideoCapture followed by
// StartVideoCapture is not safe to run twice concurrently on one V4L2 device.
var resolutionMu sync.Mutex

func (r *Runtime) buildRouter() http.Handler {
	mux := http.NewServeMux()

	/* Media streams */
	mux.HandleFunc("/api/video/stream", r.handleVideoStream)
	mux.HandleFunc("/api/video/screenshot", r.handleScreenshot)

	/* HID input (USB gadget) */
	mux.HandleFunc("/ws/hid", func(w http.ResponseWriter, req *http.Request) {
		if r.HID == nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("HID gadget not available"))
			return
		}
		hid.WebSocketHandler(r.HID)(w, req)
	})
	mux.HandleFunc("/api/hid/reset", r.handleHIDReset)
	mux.HandleFunc("/api/hid/jiggler", r.handleHIDJiggler)

	/* ISO mount (f_mass_storage) */
	mux.HandleFunc("/api/iso/status", r.handleISOStatus)
	mux.HandleFunc("/api/iso/list", r.handleISOList)
	mux.HandleFunc("/api/iso/mount", r.handleISOMount)

	/* Session auth (login form + session endpoints) */
	mux.HandleFunc("/login", serveLoginPage)
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/api/logout", handleLogout)

	/* Status & capture control */
	mux.HandleFunc("/api/status", r.handleStatus)
	mux.HandleFunc("/api/video/resolutions", r.handleResolutions)
	mux.HandleFunc("/api/video/resolution", r.handleSetResolution)

	/* Web console (embedded) */
	mux.Handle("/", webHandler())

	// /healthz and the public paths bypass session auth
	return noCacheAPI(healthWrap(sessionAuth(mux)))
}

func healthWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok\n"))
			return
		}
		next.ServeHTTP(w, req)
	})
}

// noCacheAPI stops browsers from caching API responses; the UI polls a few of
// these and a cached /api/status would freeze the on-screen indicators.
func noCacheAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if len(req.URL.Path) >= 5 && req.URL.Path[:5] == "/api/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, req)
	})
}

type statusResponse struct {
	VideoDevice    string `json:"video_device"`
	AudioAvailable bool   `json:"audio_available"`
	AudioNote      string `json:"audio_note"`
	HIDAvailable   bool   `json:"hid_available"`
	UDCState       string `json:"udc_state"`
	ISOFile        string `json:"iso_file"`
	Capturing      bool   `json:"capturing"`
	StreamInfo     string `json:"stream_info"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	FPS            int    `json:"fps"`
	BitrateBps     int64  `json:"bitrate_bps"`
}

func (r *Runtime) handleStatus(w http.ResponseWriter, req *http.Request) {
	status := statusResponse{
		AudioAvailable: false,
		AudioNote:      "audio unavailable: MS2109 exposes no PCM",
		HIDAvailable:   r.HID != nil,
	}
	if r.Capture != nil {
		width, height, fps := r.Capture.GetCaptureDims()
		status.VideoDevice = r.Capture.Config.VideoDeviceName
		status.Capturing = r.Capture.IsCapturing()
		status.StreamInfo = r.Capture.GetStreamInfo()
		status.Width = width
		status.Height = height
		status.FPS = fps
		status.BitrateBps = r.Capture.GetBitrateBps()
	} else {
		status.StreamInfo = "video offline: capture device unavailable"
	}
	if r.Gadget != nil {
		status.UDCState = r.Gadget.UDCState()
		status.ISOFile = r.Gadget.ISOFile()
	}
	writeJSON(w, http.StatusOK, status)
}

type resolutionEntry struct {
	Width  int   `json:"width"`
	Height int   `json:"height"`
	FPS    []int `json:"fps"`
}

// handleResolutions serves the fixed stream menu (already intersected with
// the device's advertised modes at startup).
func (r *Runtime) handleResolutions(w http.ResponseWriter, req *http.Request) {
	if r.Capture == nil {
		writeJSON(w, http.StatusOK, []resolutionEntry{})
		return
	}
	entries := make([]resolutionEntry, 0, len(r.Capture.GetStreamMenu()))
	for _, m := range r.Capture.GetStreamMenu() {
		entries = append(entries, resolutionEntry{
			Width:  m.Width,
			Height: m.Height,
			FPS:    m.FPS,
		})
	}
	writeJSON(w, http.StatusOK, entries)
}

// With no video device the stream and screenshot endpoints answer 503.
func (r *Runtime) handleVideoStream(w http.ResponseWriter, req *http.Request) {
	if r.Capture == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("video offline"))
		return
	}
	r.Capture.ServeVideoStream(w, req)
}

func (r *Runtime) handleScreenshot(w http.ResponseWriter, req *http.Request) {
	if r.Capture == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("video offline"))
		return
	}
	r.Capture.ServeScreenshot(w, req)
}

func (r *Runtime) handleSetResolution(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("POST required"))
		return
	}
	if r.Capture == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("video offline"))
		return
	}

	width, err := intParam(req, "width")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	height, err := intParam(req, "height")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	fps, err := intParam(req, "fps")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	resolutionMu.Lock()
	defer resolutionMu.Unlock()

	logging.Infof("Changing capture resolution to %dx%d @ %dfps", width, height, fps)
	err = r.Capture.ChangeResolution(&capture.CaptureResolution{
		Width:  width,
		Height: height,
		FPS:    fps,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"stream_info": r.Capture.GetStreamInfo(),
	})
}

/* HID control */

func (r *Runtime) handleHIDReset(w http.ResponseWriter, req *http.Request) {
	if r.HID == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("HID gadget not available"))
		return
	}
	r.HID.ReleaseAll()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleHIDJiggler: GET = state, POST?enable=1|0 = start/stop.
func (r *Runtime) handleHIDJiggler(w http.ResponseWriter, req *http.Request) {
	if r.HID == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("HID gadget not available"))
		return
	}
	if req.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]bool{"running": r.HID.IsJigglerEnabled()})
		return
	}
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("GET or POST required"))
		return
	}
	enable := req.URL.Query().Get("enable") != "0"
	if enable {
		r.HID.StartJiggler()
	} else {
		r.HID.StopJiggler()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"running": r.HID.IsJigglerEnabled()})
}

/* ISO mount (f_mass_storage) */

func (r *Runtime) handleISOStatus(w http.ResponseWriter, req *http.Request) {
	isoFile, udcState := "", ""
	if r.Gadget != nil {
		isoFile = r.Gadget.ISOFile()
		udcState = r.Gadget.UDCState()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"iso_file":  isoFile,
		"udc_state": udcState,
	})
}

func (r *Runtime) handleISOList(w http.ResponseWriter, req *http.Request) {
	writeJSON(w, http.StatusOK, iso.List())
}

// handleISOMount: POST /api/iso/mount?file=<path> swaps the backing file; an
// empty file unmounts. The UDC rebinds, so the target re-enumerates the CD-ROM.
func (r *Runtime) handleISOMount(w http.ResponseWriter, req *http.Request) {
	if r.Gadget == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("gadget not available"))
		return
	}
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("POST required"))
		return
	}
	path := req.URL.Query().Get("file")
	if path != "" {
		if !strings.HasSuffix(path, ".iso") {
			writeError(w, http.StatusBadRequest, fmt.Errorf("only .iso files are allowed"))
			return
		}
		if _, err := os.Stat(path); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("file not found: %s", path))
			return
		}
	}
	if err := r.Gadget.SetISOFile(path); err != nil {
		logging.Errorf("ISO mount failed: %v", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	logging.Infof("ISO mounted: %s", orNone(path))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"iso_file": path,
	})
}

func intParam(req *http.Request, name string) (int, error) {
	raw := req.URL.Query().Get(name)
	if raw == "" {
		return 0, fmt.Errorf("missing parameter %s", name)
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid value for %s: %s", name, raw)
	}
	return value, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logging.Warnf("failed to write JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

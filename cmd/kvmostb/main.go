package main

/*
	KVMoSTB: KVM-over-IP for the HG680-FJ STB.

	HID input over a USB HID gadget (configfs) on the dwc2 OTG port,
	ISO mount via f_mass_storage, video over the MS2109 capture dongle.
	License and upstream credits: see README.md.
*/

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kvmostb/internal/capture"
	"kvmostb/internal/gadget"
	"kvmostb/internal/hid"
	"kvmostb/internal/iso"
	"kvmostb/internal/logging"
	"log"
)

var (
	flagVideoDevice = flag.String("video", "", "Video capture device node, e.g. /dev/video1 (auto-detected when empty)")
	flagListen      = flag.String("listen", listenDefault(), "HTTP listen address (default: LISTEN_ADDRESS:LISTEN_PORT env, then 0.0.0.0:8080)")
	flagJPEGQuality = flag.Int("jpeg-quality", 0, "Re-encode frames at this JPEG quality (5-80) to save bandwidth, 0 to pass frames through untouched")
	flagGadgetInit  = flag.Bool("gadget-init", false, "Initialize the USB HID gadget (configfs + UDC bind) and exit — used by kvmostb-gadget.service (oneshot)")
	flagLogLevel    = flag.String("log-level", "", "Log level: debug, info, warn, error (default: LOG_LEVEL env, then info)")
)

// listenDefault builds the -listen flag default from the environment:
// LISTEN_ADDRESS (default 0.0.0.0) + LISTEN_PORT (default 8080).
func listenDefault() string {
	addr := os.Getenv("LISTEN_ADDRESS")
	if addr == "" {
		addr = "0.0.0.0"
	}
	port := os.Getenv("LISTEN_PORT")
	if port == "" {
		port = "8080"
	}
	return net.JoinHostPort(addr, port)
}

func main() {
	flag.Parse()

	lvl := logging.LevelInfo
	if *flagLogLevel != "" {
		lvl, _ = logging.ParseLevel(*flagLogLevel)
	} else if env := os.Getenv("LOG_LEVEL"); env != "" {
		lvl, _ = logging.ParseLevel(env)
	}
	logging.SetFlags(log.Ldate | log.Ltime)
	logging.SetLevel(lvl)
	logging.Infof("KVMoSTB starting up (log level=%s)", lvl)

	if *flagGadgetInit {
		runGadgetInit()
		return
	}

	runDaemon()
}

// runGadgetInit brings up the USB gadget and exits (oneshot unit). The gadget
// starts without mass storage; the UI mounts ISOs on demand.
func runGadgetInit() {
	g, err := gadget.Init(gadget.Options{})
	if err != nil {
		fatal("gadget init failed: %v", err)
	}
	logging.Infof("gadget init done (udc=%s state=%s iso=%s)", g.UDC(), g.UDCState(), orNone(g.ISOFile()))
}

func runDaemon() {
	/* A missing video device leaves the service running with video offline. */
	videoDev := *flagVideoDevice
	if videoDev == "" {
		var err error
		videoDev, err = capture.GetDefaultCaptureDevice()
		if err != nil {
			logging.Errorf("video device detection failed: %v - video offline", err)
			videoDev = "unavailable"
		}
	}
	logging.Infof("Video: %s", videoDev)

	/* Capture failures leave input and ISO working; /api/status reports video offline. */
	var cam *capture.Instance
	cam, err := capture.NewInstance(&capture.Config{
		VideoDeviceName: videoDev,
		VideoConfig: &capture.VideoConfig{
			UseJPEGCompression:     *flagJPEGQuality > 0,
			JPEGCompressionQuality: *flagJPEGQuality,
		},
	})
	if err != nil {
		logging.Errorf("video capture unavailable (%s): %v - video offline", videoDev, err)
		cam = nil
	} else if err := cam.StartVideoCapture(&capture.DefaultResolution); err != nil {
		logging.Errorf("video capture failed to start: %v - video offline", err)
		cam = nil
	} else {
		logging.Infof("Capturing: %s", cam.GetStreamInfo())
	}

	/* The oneshot unit may already have bound the gadget; Init is idempotent. */
	g, err := gadget.Init(gadget.Options{})
	if err != nil {
		fatal("gadget init failed: %v", err)
	}
	if err := g.Open(); err != nil {
		fatal("failed to open HID gadget devices: %v", err)
	}
	logging.Infof("HID gadget: udc=%s state=%s iso=%s", g.UDC(), g.UDCState(), orNone(g.ISOFile()))

	/* ISO_AUTO_MOUNT=true mounts the newest ISO at boot; off by default. */
	if env := os.Getenv("ISO_AUTO_MOUNT"); env == "true" || env == "1" {
		if entries := iso.List(); len(entries) > 0 {
			if err := g.SetISOFile(entries[0].Path); err != nil {
				logging.Errorf("ISO auto-mount failed for %s: %v", entries[0].Path, err)
			} else {
				logging.Infof("ISO auto-mounted: %s", entries[0].Path)
			}
		} else {
			logging.Warnf("ISO_AUTO_MOUNT=true but no ISO found in search roots")
		}
	}

	runtime := &Runtime{
		Capture: cam,
		HID:     hid.NewController(nil, gadget.NewHIDWriter(g)),
		Gadget:  g,
	}

	server := &http.Server{
		Addr:    *flagListen,
		Handler: runtime.buildRouter(),
	}

	go func() {
		logging.Infof("Web interface listening on http://%s", *flagListen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("http server error: %v", err)
		}
	}()

	/* Wait for SIGINT/SIGTERM and release the hardware cleanly. Leaving the
	   V4L2 device open makes the next start fail with EBUSY. */
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logging.Infoln("Shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)

	if cam != nil {
		cam.Close()
	}
	logging.Infoln("Bye")
}

// fatal logs at error level and exits 1 (keeps log.Fatalf semantics).
func fatal(format string, args ...any) {
	logging.Errorf(format, args...)
	os.Exit(1)
}

func orNone(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}

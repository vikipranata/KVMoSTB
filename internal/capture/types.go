package capture

/*
	Capture types and configuration. There are no audio fields: the MS2109
	exposes no PCM.
*/

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vladimirvivien/go4vl/device"
	"github.com/vladimirvivien/go4vl/v4l2"
)

// FormatInfo stores the size and fps info advertised by the device.
type FormatInfo struct {
	Format string
	Sizes  []SizeInfo
}

type SizeInfo struct {
	Width  int
	Height int
	FPS    []int
}

// CaptureResolution is the stream mode to open the video device with.
type CaptureResolution struct {
	Width  int
	Height int
	FPS    int
}

// VideoConfig holds MJPEG streaming options.
type VideoConfig struct {
	// UseJPEGCompression re-encodes each frame in software at the given
	// quality (5-80). Off by default: the MS2109 already compresses.
	UseJPEGCompression     bool
	JPEGCompressionQuality int
}

// Config is the static capture configuration.
type Config struct {
	VideoDeviceName string // e.g. /dev/video1
	VideoConfig     *VideoConfig
}

// Instance is a running video capture.
type Instance struct {
	/* Runtime configuration */
	Config               *Config
	SupportedResolutions []FormatInfo // raw device capabilities (v4l2-ctl)
	StreamMenu           []SizeInfo   // fixed menu after intersection (see modes.go)
	Capturing            bool

	camera             *device.Device
	cameraStartContext context.CancelFunc
	frames_buff        <-chan []byte
	pixfmt             v4l2.FourCCType
	width              int
	height             int
	fps                int
	streamInfo         string

	/* Live bitrate (bits/s, exponential moving average over streamed frames).
	   bitrateBps is atomic; lastFrameAt is only touched by the active
	   streamer goroutine and reset in StopVideoCapture. */
	bitrateBps  atomic.Int64
	lastFrameAt time.Time

	streamMu            sync.Mutex
	activeVideoConsumer *videoConsumer
}

type videoConsumer struct {
	takeover chan struct{}
	done     chan struct{}
}

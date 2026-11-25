package capture

/*
	V4L2 MJPEG capture over a USB UVC device: device probing, single-consumer
	takeover, and the MJPEG stream loop with live bitrate accounting.
	There is no audio path.
*/

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"kvmostb/internal/logging"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vladimirvivien/go4vl/device"
	"github.com/vladimirvivien/go4vl/v4l2"
)

//go:embed stream_takeover.jpg
var endOfStreamJPG []byte

// StartVideoCapture opens the device and starts the frame stream.
// The resolution must be a mode from the fixed stream menu.
func (i *Instance) StartVideoCapture(res *CaptureResolution) error {
	if i.Capturing {
		return fmt.Errorf("video capture already started")
	}
	if res == nil {
		return fmt.Errorf("resolution not provided")
	}
	if err := ValidateResolution(i.StreamMenu, res); err != nil {
		return err
	}
	frameRate := res.FPS
	if frameRate == 0 {
		frameRate = DefaultResolution.FPS
	}
	buffSize := 2 // no. of frames to buffer
	devName := i.Config.VideoDeviceName

	isCaptureDev, err := CheckVideoCaptureDevice(devName)
	if err != nil {
		return fmt.Errorf("failed to check video device: %w", err)
	}
	if !isCaptureDev {
		return fmt.Errorf("device %s is not a video capture device", devName)
	}

	// Retry opening the device up to 5 times with increasing delay
	var camera *device.Device
	maxRetries := 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		camera, err = device.Open(devName,
			device.WithIOType(v4l2.IOTypeMMAP),
			device.WithPixFormat(v4l2.PixFormat{
				PixelFormat: v4l2.PixelFmtMJPEG,
				Width:       uint32(res.Width),
				Height:      uint32(res.Height),
				Field:       v4l2.FieldAny,
			}),
			device.WithFPS(uint32(frameRate)),
			device.WithBufferSize(uint32(buffSize)),
		)
		if err == nil {
			break
		}

		// Check if it's a "device or resource busy" error
		if strings.Contains(err.Error(), "device or resource busy") && attempt < maxRetries-1 {
			logging.Warnf("Device busy, retrying in %d seconds (attempt %d/%d)", attempt+1, attempt+1, maxRetries)
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}

		logging.Errorf("open video device: %v", err)
		return fmt.Errorf("failed to open video device: %w", err)
	}

	i.camera = camera
	caps := camera.Capability()
	logging.Infof("device [%s] opened", devName)
	logging.Debugf("device info: %s", caps.String())

	currFmt, err := camera.GetPixFormat()
	if err != nil {
		return fmt.Errorf("failed to get current pixel format: %w", err)
	}
	logging.Infof("Current format: %s", currFmt)

	// Store format info
	i.pixfmt = currFmt.PixelFormat
	i.width = int(currFmt.Width)
	i.height = int(currFmt.Height)
	i.fps = frameRate

	i.streamInfo = fmt.Sprintf("%s - %s [%dx%d] %d fps",
		caps.Card,
		v4l2.PixelFormats[currFmt.PixelFormat],
		currFmt.Width, currFmt.Height, frameRate,
	)

	ctx, cancel := context.WithCancel(context.TODO())
	if err := camera.Start(ctx); err != nil {
		logging.Errorf("stream capture: %s", err)
		os.Exit(1)
	}
	i.cameraStartContext = cancel

	i.frames_buff = camera.GetOutput()

	logging.Infof("device capture started (buffer size set %d)", camera.BufferCount())
	i.Capturing = true
	return nil
}

// ServeVideoStream streams MJPEG over HTTP multipart to a single client.
// A new client kicks out the previous one.
func (i *Instance) ServeVideoStream(w http.ResponseWriter, req *http.Request) {
	consumer, tookOver := i.beginVideoConsumer()
	defer i.endVideoConsumer(consumer)

	if tookOver {
		logging.Infof("another client already connected, taking over the stream...")
	}

	if err := i.streamMJPEG(w, req, consumer); err != nil {
		logging.Errorf("video stream: %v", err)
	}
}

func isJPEG(frame []byte) bool {
	if len(frame) < 4 {
		return false
	}
	if bytes.HasPrefix(frame, []byte{0xFF, 0xD8}) && bytes.HasSuffix(frame, []byte{0xFF, 0xD9}) {
		return true
	}
	// Fallback: look for SOI anywhere and EOI after it
	start := bytes.Index(frame, []byte{0xFF, 0xD8})
	end := bytes.LastIndex(frame, []byte{0xFF, 0xD9})
	return start >= 0 && end > start
}

func (i *Instance) streamMJPEG(w http.ResponseWriter, req *http.Request, consumer *videoConsumer) error {
	mimeWriter := multipart.NewWriter(w)
	w.Header().Set("Content-Type", fmt.Sprintf("multipart/x-mixed-replace; boundary=%s", mimeWriter.Boundary()))
	partHeader := make(textproto.MIMEHeader)
	partHeader.Add("Content-Type", "image/jpeg")

	// Chrome cannot decode the first MS2109 frame; discard it.
	if i.frames_buff != nil {
		select {
		case <-i.frames_buff:
		default:
		}
	}

	i.lastFrameAt = time.Time{}
	var frame []byte
	var partWriter io.Writer
	var err error
	for frame = range i.frames_buff {
		select {
		case <-req.Context().Done():
			return nil
		case <-consumer.takeover:
			return writeTakeoverFrame(mimeWriter)
		default:
		}

		// Drain a buffered frame to reduce latency when congested
		select {
		case f := <-i.frames_buff:
			frame = f
		default:
		}

		if len(frame) == 0 {
			logging.Debugf("skipping empty frame")
			continue
		}

		if !isJPEG(frame) {
			continue
		}

		partWriter, err = mimeWriter.CreatePart(partHeader)
		if err != nil {
			logging.Errorf("multipart writer: %s", err)
			return err
		}

		// Software frame compression (optional)
		if i.Config.VideoConfig.UseJPEGCompression {
			img, err := jpeg.Decode(bytes.NewReader(frame))
			if err != nil {
				logging.Errorf("jpeg decode: %s", err)
				continue
			}

			var buf bytes.Buffer
			err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: i.Config.VideoConfig.JPEGCompressionQuality})
			if err != nil {
				logging.Errorf("jpeg re-encode: %s", err)
				continue
			}
			frame = buf.Bytes()
		}

		if _, err = partWriter.Write(frame); err != nil {
			if errors.Is(err, syscall.EPIPE) {
				// broken pipe, the client browser has exited
				return nil
			}
			if errors.Is(err, http.ErrAbortHandler) {
				return nil
			}
			logging.Errorf("write image: %s", err)
		}

		// Live bitrate: exponential moving average of per-frame rate
		now := time.Now()
		if !i.lastFrameAt.IsZero() {
			if dt := now.Sub(i.lastFrameAt).Seconds(); dt > 0 {
				inst := float64(len(frame)) * 8 / dt
				i.bitrateBps.Store(int64(float64(i.bitrateBps.Load())*0.8 + inst*0.2))
			}
		}
		i.lastFrameAt = now

		// Flush so the frame reaches the client immediately
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		select {
		case <-req.Context().Done():
			return nil
		case <-consumer.takeover:
			return writeTakeoverFrame(mimeWriter)
		default:
		}
	}
	return nil
}

func writeTakeoverFrame(mimeWriter *multipart.Writer) error {
	endFrameHeader := make(textproto.MIMEHeader)
	endFrameHeader.Add("Content-Type", "image/jpeg")
	endFrameHeader.Add("Content-Length", fmt.Sprint(len(endOfStreamJPG)))
	partWriter, err := mimeWriter.CreatePart(endFrameHeader)
	if err == nil {
		if _, writeErr := partWriter.Write(endOfStreamJPG); writeErr != nil {
			return writeErr
		}
	}
	logging.Infof("video stream taken over by another client, exiting...")
	return nil
}

// beginVideoConsumer claims the single video stream for this consumer and
// kicks out the previous one (if any).
func (i *Instance) beginVideoConsumer() (*videoConsumer, bool) {
	consumer := &videoConsumer{
		takeover: make(chan struct{}),
		done:     make(chan struct{}),
	}

	i.streamMu.Lock()
	previous := i.activeVideoConsumer
	i.activeVideoConsumer = consumer
	i.streamMu.Unlock()

	if previous != nil {
		close(previous.takeover)
		select {
		case <-previous.done:
		case <-time.After(3 * time.Second):
			logging.Warnf("previous video consumer did not exit before timeout, continuing takeover...")
		}
	}

	return consumer, previous != nil
}

func (i *Instance) endVideoConsumer(consumer *videoConsumer) {
	i.streamMu.Lock()
	if i.activeVideoConsumer == consumer {
		i.activeVideoConsumer = nil
	}
	i.streamMu.Unlock()
	close(consumer.done)
}

// StopVideoCapture stops the video capture and closes the camera device.
func (i *Instance) StopVideoCapture() error {
	if i.camera != nil {
		i.cameraStartContext()
		err := i.camera.Close()
		if err != nil {
			return err
		}
		i.camera = nil
		time.Sleep(300 * time.Millisecond)
		logging.Infof("video capture stopped")
	} else {
		logging.Infof("video capture is not running")
	}
	i.Capturing = false
	i.bitrateBps.Store(0)
	i.lastFrameAt = time.Time{}
	return nil
}

// CheckVideoCaptureDevice checks if the given video device is a capture device.
func CheckVideoCaptureDevice(device string) (bool, error) {
	cmd := exec.Command("v4l2-ctl", "--device", device, "--all")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to execute v4l2-ctl: %w", err)
	}
	return strings.Contains(string(output), "Video Capture"), nil
}

// GetDefaultVideoDevice returns the first available video capture device.
func GetDefaultVideoDevice() (string, error) {
	for i := 0; i < 10; i++ {
		device := fmt.Sprintf("/dev/video%d", i)
		isCapture, err := CheckVideoCaptureDevice(device)
		if err != nil {
			continue
		}
		if isCapture {
			return device, nil
		}
	}
	return "", fmt.Errorf("no video capture device found")
}

// GetDefaultCaptureDevice returns the first capture device that advertises a mode
// of the fixed menu, which finds the MS2109 whatever its /dev/videoN number. With
// no match it returns the first capture device so the caller gets a menu error.
func GetDefaultCaptureDevice() (string, error) {
	var fallback string
	for i := 0; i < 10; i++ {
		device := fmt.Sprintf("/dev/video%d", i)
		isCapture, err := CheckVideoCaptureDevice(device)
		if err != nil || !isCapture {
			continue
		}
		if fallback == "" {
			fallback = device
		}
		formats, err := GetV4L2FormatInfo(device)
		if err != nil {
			continue
		}
		for _, f := range formats {
			for _, sz := range f.Sizes {
				switch {
				case sz.Width == 1920 && sz.Height == 1080,
					sz.Width == 1280 && sz.Height == 720,
					sz.Width == 640 && sz.Height == 480:
					return device, nil
				}
			}
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("no video capture device found")
}

// GetV4L2FormatInfo runs v4l2-ctl and parses the advertised formats, sizes
// and frame rates.
func GetV4L2FormatInfo(devicePath string) ([]FormatInfo, error) {
	cmd := exec.Command("v4l2-ctl", "--list-formats-ext", "-d", devicePath)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	var formats []FormatInfo
	var currentFormat *FormatInfo
	var currentSize *SizeInfo
	scanner := bufio.NewScanner(&out)

	formatRegex := regexp.MustCompile(`\[(\d+)\]: '(\S+)'`)
	sizeRegex := regexp.MustCompile(`Size: Discrete (\d+)x(\d+)`)
	intervalRegex := regexp.MustCompile(`Interval: Discrete (\d+\.\d+)s \((\d+\.\d+) fps\)`)

	flushSize := func() {
		if currentFormat != nil && currentSize != nil {
			currentFormat.Sizes = append(currentFormat.Sizes, *currentSize)
		}
		currentSize = nil
	}
	flushFormat := func() {
		flushSize()
		if currentFormat != nil {
			formats = append(formats, *currentFormat)
		}
		currentFormat = nil
	}

	for scanner.Scan() {
		line := scanner.Text()

		if matches := formatRegex.FindStringSubmatch(line); matches != nil {
			flushFormat()
			currentFormat = &FormatInfo{Format: matches[2]}
			continue
		}

		if matches := sizeRegex.FindStringSubmatch(line); matches != nil {
			flushSize()
			width, _ := strconv.Atoi(matches[1])
			height, _ := strconv.Atoi(matches[2])
			currentSize = &SizeInfo{Width: width, Height: height}
			continue
		}

		if matches := intervalRegex.FindStringSubmatch(line); matches != nil && currentSize != nil {
			fps, _ := strconv.ParseFloat(matches[2], 32)
			currentSize.FPS = append(currentSize.FPS, int(fps))
		}
	}

	flushFormat()

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return formats, nil
}

// CaptureScreenshot captures a single frame and returns it as JPEG.
// Uses the live stream when capturing, otherwise temporarily opens the device.
func (i *Instance) CaptureScreenshot() ([]byte, error) {
	// If the device is already capturing, grab a frame from the current stream
	if i.Capturing && i.frames_buff != nil {
		for j := 0; j < 5; j++ {
			select {
			case <-i.frames_buff:
			case <-time.After(1 * time.Second):
			}
		}
		select {
		case frame := <-i.frames_buff:
			if len(frame) > 0 && isJPEG(frame) {
				return frame, nil
			}
			return nil, fmt.Errorf("invalid frame received from stream")
		case <-time.After(5 * time.Second):
			return nil, fmt.Errorf("timeout waiting for frame from active stream")
		}
	}

	// Device is not capturing: temporarily open it
	devName := i.Config.VideoDeviceName

	isCaptureDev, err := CheckVideoCaptureDevice(devName)
	if err != nil {
		return nil, fmt.Errorf("failed to check video device: %w", err)
	}
	if !isCaptureDev {
		return nil, fmt.Errorf("device %s is not a video capture device", devName)
	}

	res := DefaultResolution
	for _, m := range i.StreamMenu {
		if m.Width == res.Width && m.Height == res.Height {
			res.FPS = m.FPS[0]
			break
		}
	}

	camera, err := device.Open(devName,
		device.WithIOType(v4l2.IOTypeMMAP),
		device.WithPixFormat(v4l2.PixFormat{
			PixelFormat: v4l2.PixelFmtMJPEG,
			Width:       uint32(res.Width),
			Height:      uint32(res.Height),
			Field:       v4l2.FieldAny,
		}),
		device.WithFPS(uint32(res.FPS)),
		device.WithBufferSize(2),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to open video device for screenshot: %w", err)
	}
	defer camera.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := camera.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start capture: %w", err)
	}

	frames := camera.GetOutput()

	// Flush the first 5 frames as they may be blank/invalid
	logging.Debugf("flushing first 5 frames from newly opened device...")
	for n := 0; n < 5; n++ {
		select {
		case <-frames:
		case <-ctx.Done():
			return nil, fmt.Errorf("timeout flushing initial frames")
		}
	}

	select {
	case frame := <-frames:
		if len(frame) > 0 && isJPEG(frame) {
			logging.Debugf("screenshot captured from device")
			return frame, nil
		}
		return nil, fmt.Errorf("invalid frame captured")
	case <-ctx.Done():
		return nil, fmt.Errorf("timeout capturing screenshot")
	}
}

// ServeScreenshot serves a single JPEG screenshot via HTTP.
func (i *Instance) ServeScreenshot(w http.ResponseWriter, req *http.Request) {
	screenshot, err := i.CaptureScreenshot()
	if err != nil {
		logging.Errorf("screenshot: %v", err)

		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusInternalServerError)

		// Simple black placeholder
		img := image.NewRGBA(image.Rect(0, 0, 320, 240))
		for y := 0; y < 240; y++ {
			for x := 0; x < 320; x++ {
				img.Set(x, y, image.Black)
			}
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err == nil {
			w.Write(buf.Bytes())
		}
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", fmt.Sprint(len(screenshot)))
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusOK)
	w.Write(screenshot)
}

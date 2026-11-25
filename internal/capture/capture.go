package capture

/*
	USB capture instance: V4L2 capture lifecycle and the fixed stream menu.
	Resolutions are validated against the menu in modes.go, not the raw device list.
*/

import (
	"fmt"
	"os"
	"time"
)

func NewInstance(config *Config) (*Instance, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	if config.VideoConfig == nil {
		config.VideoConfig = &VideoConfig{}
	}

	if _, err := os.Stat(config.VideoDeviceName); os.IsNotExist(err) {
		return nil, fmt.Errorf("video device %s does not exist", config.VideoDeviceName)
	} else if err != nil {
		return nil, fmt.Errorf("failed to check video device: %w", err)
	}

	isValidDevice, err := CheckVideoCaptureDevice(config.VideoDeviceName)
	if err != nil {
		return nil, fmt.Errorf("failed to check video device: %w", err)
	}
	if !isValidDevice {
		return nil, fmt.Errorf("device %s is not a video capture device", config.VideoDeviceName)
	}

	formatInfo, err := GetV4L2FormatInfo(config.VideoDeviceName)
	if err != nil {
		return nil, fmt.Errorf("failed to get video device format info: %w", err)
	}
	if len(formatInfo) == 0 {
		return nil, fmt.Errorf("no supported formats found for device %s", config.VideoDeviceName)
	}

	menu := BuildStreamMenu(formatInfo)
	if len(menu) == 0 {
		return nil, fmt.Errorf("device %s advertises none of the fixed stream menu modes",
			config.VideoDeviceName)
	}

	return &Instance{
		Config:               config,
		SupportedResolutions: formatInfo,
		StreamMenu:           menu,
		Capturing:            false,
	}, nil
}

// GetStreamInfo returns the stream description with the live bitrate.
func (i *Instance) GetStreamInfo() string {
	bps := float64(i.bitrateBps.Load())
	return fmt.Sprintf("%s | live %.1f Mbps", i.streamInfo, bps/1e6)
}

// GetBitrateBps returns the current live stream bitrate in bits/s.
func (i *Instance) GetBitrateBps() int64 {
	return i.bitrateBps.Load()
}

func (i *Instance) IsCapturing() bool {
	return i.Capturing
}

func (i *Instance) Close() error {
	if i.camera != nil {
		return i.StopVideoCapture()
	}
	return nil
}

// GetStreamMenu returns the fixed stream menu offered to the UI.
func (i *Instance) GetStreamMenu() []SizeInfo {
	return i.StreamMenu
}

// GetCaptureDims returns the active capture width, height and frame rate.
func (i *Instance) GetCaptureDims() (width int, height int, fps int) {
	return i.width, i.height, i.fps
}

// ChangeResolution stops the current capture and restarts it with the
// requested mode. The mode must be in the fixed stream menu.
func (i *Instance) ChangeResolution(newResolution *CaptureResolution) error {
	if err := ValidateResolution(i.StreamMenu, newResolution); err != nil {
		return err
	}

	if err := i.StopVideoCapture(); err != nil {
		return fmt.Errorf("failed to stop capture: %w", err)
	}

	// The device needs a moment to release its buffers.
	time.Sleep(500 * time.Millisecond)

	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = i.StartVideoCapture(newResolution)
		if err == nil {
			return nil
		}
		if attempt < 2 {
			fmt.Println("Error: ", err, " - Retrying to start capture with new resolution...")
			time.Sleep(500 * time.Millisecond)
		}
	}
	return fmt.Errorf("failed to start capture with new resolution after 3 attempts: %w", err)
}

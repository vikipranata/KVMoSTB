package capture

/*
	Fixed stream menu: the UI offers 640x480, 1280x720 and 1920x1080 at 25, 30
	and 60 fps, limited to what the capture device advertises.

	The MS2109 advertises (MJPG):
	  1920x1080 -> 60/30/25/20/10 fps
	  1280x720  -> 60/50/30/20/10 fps   (no 25)
	  640x480   -> 60/50/30/20/10 fps   (no 25)

	The effective menu is the intersection:
	  1920x1080 -> 25, 30, 60
	  1280x720  -> 30, 60
	  640x480   -> 30, 60

	The default stream is 1280x720 at 30 fps.
*/

import (
	"fmt"
	"sort"
)

// FixedResolutions is the resolution set offered by the UI, largest first.
var FixedResolutions = []struct {
	Width  int
	Height int
}{{1920, 1080}, {1280, 720}, {640, 480}}

// FixedFPS is the fps set offered by the UI.
var FixedFPS = []int{25, 30, 60}

// DefaultResolution is the stream mode used at startup.
var DefaultResolution = CaptureResolution{Width: 1280, Height: 720, FPS: 30}

// BuildStreamMenu intersects the fixed menu with the device's advertised modes,
// dropping sizes the device lacks or offers with none of the fixed frame rates.
func BuildStreamMenu(deviceInfo []FormatInfo) []SizeInfo {
	type wh struct{ w, h int }
	advertised := make(map[wh]map[int]bool)
	for _, f := range deviceInfo {
		if f.Format != "MJPG" && f.Format != "MJPEG" && f.Format != "mjpeg" {
			continue
		}
		for _, s := range f.Sizes {
			key := wh{s.Width, s.Height}
			set, ok := advertised[key]
			if !ok {
				set = make(map[int]bool)
				advertised[key] = set
			}
			for _, fps := range s.FPS {
				set[fps] = true
			}
		}
	}

	var menu []SizeInfo
	for _, r := range FixedResolutions {
		set, ok := advertised[wh{r.Width, r.Height}]
		if !ok {
			continue
		}
		var fps []int
		for _, f := range FixedFPS {
			if set[f] {
				fps = append(fps, f)
			}
		}
		if len(fps) == 0 {
			continue
		}
		sort.Ints(fps)
		menu = append(menu, SizeInfo{Width: r.Width, Height: r.Height, FPS: fps})
	}
	return menu
}

// ValidateResolution rejects modes outside the fixed menu so the device is never
// opened with an unknown mode.
func ValidateResolution(menu []SizeInfo, r *CaptureResolution) error {
	if r == nil {
		return fmt.Errorf("resolution not provided")
	}
	for _, m := range menu {
		if m.Width == r.Width && m.Height == r.Height {
			for _, fps := range m.FPS {
				if fps == r.FPS {
					return nil
				}
			}
			return fmt.Errorf("%dx%d does not offer %d fps (offered: %v)",
				m.Width, m.Height, r.FPS, m.FPS)
		}
	}
	return fmt.Errorf("%dx%d @ %d fps is not in the stream menu (offered: %v)",
		r.Width, r.Height, r.FPS, menu)
}

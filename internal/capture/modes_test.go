package capture

import "testing"

// Real MS2109 MJPG advertisement (v4l2-ctl, /dev/video1, 2026-11-20).
func ms2109DeviceInfo() []FormatInfo {
	return []FormatInfo{{
		Format: "MJPG",
		Sizes: []SizeInfo{
			{1920, 1080, []int{60, 30, 25, 20, 10}},
			{1600, 1200, []int{60, 30, 25, 20, 10}},
			{1360, 768, []int{60, 30, 25, 20, 10}},
			{1280, 720, []int{60, 50, 30, 20, 10}},
			{720, 480, []int{60, 50, 30, 20, 10}},
			{640, 480, []int{60, 50, 30, 20, 10}},
		},
	}}
}

func TestBuildStreamMenu_MS2109(t *testing.T) {
	menu := BuildStreamMenu(ms2109DeviceInfo())
	want := []SizeInfo{
		{1920, 1080, []int{25, 30, 60}},
		{1280, 720, []int{30, 60}},
		{640, 480, []int{30, 60}},
	}
	if len(menu) != len(want) {
		t.Fatalf("menu len = %d, want %d: %v", len(menu), len(want), menu)
	}
	for i := range want {
		if menu[i].Width != want[i].Width || menu[i].Height != want[i].Height {
			t.Fatalf("menu[%d] = %dx%d, want %dx%d", i, menu[i].Width, menu[i].Height, want[i].Width, want[i].Height)
		}
		if len(menu[i].FPS) != len(want[i].FPS) {
			t.Fatalf("menu[%d].fps = %v, want %v", i, menu[i].FPS, want[i].FPS)
		}
		for j := range want[i].FPS {
			if menu[i].FPS[j] != want[i].FPS[j] {
				t.Fatalf("menu[%d].fps = %v, want %v", i, menu[i].FPS, want[i].FPS)
			}
		}
	}
}

func TestValidateResolution(t *testing.T) {
	menu := []SizeInfo{{1920, 1080, []int{25, 30, 60}}, {1280, 720, []int{30, 60}}}
	if err := ValidateResolution(menu, &CaptureResolution{1280, 720, 30}); err != nil {
		t.Fatalf("720p30 should be valid: %v", err)
	}
	if err := ValidateResolution(menu, &CaptureResolution{1280, 720, 25}); err == nil {
		t.Fatal("720p25 should be invalid (25 fps not advertised at 720p)")
	}
	if err := ValidateResolution(menu, &CaptureResolution{800, 600, 30}); err == nil {
		t.Fatal("800x600 should be invalid (not in fixed menu)")
	}
	if err := ValidateResolution(menu, nil); err == nil {
		t.Fatal("nil resolution should be invalid")
	}
}

package hid

import (
	"bytes"
	"testing"
)

// mockWriter records the reports it receives.
type mockWriter struct {
	keyboard []byte
	mouse    []byte
}

func (m *mockWriter) WriteKeyboard(r [8]byte) error {
	m.keyboard = append(m.keyboard, r[:]...)
	return nil
}

func (m *mockWriter) WriteMouse(r [6]byte) error {
	m.mouse = append(m.mouse, r[:]...)
	return nil
}

func TestKeyboardReport(t *testing.T) {
	var mw mockWriter
	c := NewController(nil, &mw)

	// Press 'A' (JS keycode 65) -> scancode 0x04 in slot 0
	if err := c.Handle(&HIDCommand{Event: EventTypeKeyPress, Keycode: 65}); err != nil {
		t.Fatal(err)
	}
	if got := mw.keyboard[0:8]; !bytes.Equal(got, []byte{0x00, 0x00, 0x04, 0, 0, 0, 0, 0}) {
		t.Fatalf("press A report = % x, want 00 00 04 00 00 00 00 00", got)
	}

	// Press Shift (16) + 'B' (66) -> modifier 0x02, A+B in slots
	if err := c.Handle(&HIDCommand{Event: EventTypeKeyPress, Keycode: 16}); err != nil {
		t.Fatal(err)
	}
	if err := c.Handle(&HIDCommand{Event: EventTypeKeyPress, Keycode: 66}); err != nil {
		t.Fatal(err)
	}
	if got := mw.keyboard[16:24]; !bytes.Equal(got, []byte{0x02, 0x00, 0x04, 0x05, 0, 0, 0, 0}) {
		t.Fatalf("shift+A+B report = % x, want 02 00 04 05 00 00 00 00", got)
	}

	// Release A, then Shift -> B stays, modifier clears
	if err := c.Handle(&HIDCommand{Event: EventTypeKeyRelease, Keycode: 65}); err != nil {
		t.Fatal(err)
	}
	if got := mw.keyboard[24:32]; !bytes.Equal(got, []byte{0x02, 0x00, 0x00, 0x05, 0, 0, 0, 0}) {
		t.Fatalf("release A report = % x, want 02 00 00 05 00 00 00 00", got)
	}
	if err := c.Handle(&HIDCommand{Event: EventTypeKeyRelease, Keycode: 16}); err != nil {
		t.Fatal(err)
	}
	if got := mw.keyboard[32:40]; !bytes.Equal(got, []byte{0x00, 0x00, 0x00, 0x05, 0, 0, 0, 0}) {
		t.Fatalf("release shift report = % x, want 00 00 00 05 00 00 00 00", got)
	}

	// ReleaseAll -> all zeros
	if err := c.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if got := mw.keyboard[40:48]; bytes.IndexByte(got, 1) != -1 {
		t.Fatalf("release all report = % x, want all zeros", got)
	}
}

// Stationary click: a button-mask change at an unchanged absolute
// position must still write a mouse report.
func TestMouseButtonChangeAtSamePosition(t *testing.T) {
	var mw mockWriter
	c := NewController(nil, &mw)

	// Move to a position, no buttons.
	if err := c.Handle(&HIDCommand{Event: EventTypeMouseMove, MouseAbsX: 100, MouseAbsY: 200, MouseMoveButtonState: 0x00}); err != nil {
		t.Fatal(err)
	}
	if len(mw.mouse) != 6 {
		t.Fatalf("after move: %d bytes written, want 6", len(mw.mouse))
	}

	// Press left button at the SAME position.
	if err := c.Handle(&HIDCommand{Event: EventTypeMouseMove, MouseAbsX: 100, MouseAbsY: 200, MouseMoveButtonState: 0x01}); err != nil {
		t.Fatal(err)
	}
	if len(mw.mouse) != 12 {
		t.Fatalf("press at same position: %d bytes written, want 12 (report dropped)", len(mw.mouse))
	}
	if mw.mouse[6] != 0x01 {
		t.Fatalf("press report button byte = %#x, want 0x01", mw.mouse[6])
	}

	// Release at the SAME position.
	if err := c.Handle(&HIDCommand{Event: EventTypeMouseMove, MouseAbsX: 100, MouseAbsY: 200, MouseMoveButtonState: 0x00}); err != nil {
		t.Fatal(err)
	}
	if len(mw.mouse) != 18 {
		t.Fatalf("release at same position: %d bytes written, want 18 (report dropped)", len(mw.mouse))
	}
	if mw.mouse[12] != 0x00 {
		t.Fatalf("release report button byte = %#x, want 0x00", mw.mouse[12])
	}

	// Same position, same mask -> nothing written.
	if err := c.Handle(&HIDCommand{Event: EventTypeMouseMove, MouseAbsX: 100, MouseAbsY: 200, MouseMoveButtonState: 0x00}); err != nil {
		t.Fatal(err)
	}
	if len(mw.mouse) != 18 {
		t.Fatalf("no-change frame: %d bytes written, want 18", len(mw.mouse))
	}
}

func TestRescaleCoord(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 0},
		{4095, 32767},
		{2048, 16387},
		{-5, 0},
		{9999, 32767},
	}
	for _, cse := range cases {
		if got := RescaleCoord(cse.in); got != cse.want {
			t.Fatalf("RescaleCoord(%d) = %d, want %d", cse.in, got, cse.want)
		}
	}
}

func TestMouseReport(t *testing.T) {
	var mw mockWriter
	c := NewController(&Config{WheelSensitivity: 3}, &mw)

	// Absolute move: web (4095, 0) -> gadget (32767, 0)
	if err := c.Handle(&HIDCommand{Event: EventTypeMouseMove, MouseAbsX: 4095, MouseAbsY: 0}); err != nil {
		t.Fatal(err)
	}
	// [buttons, Xlo, Xhi, Ylo, Yhi, wheel]
	if got := mw.mouse[0:6]; !bytes.Equal(got, []byte{0x00, 0xFF, 0x7F, 0x00, 0x00, 0x00}) {
		t.Fatalf("move report = % x, want 00 ff 7f 00 00 00", got)
	}

	// Left button press
	if err := c.Handle(&HIDCommand{Event: EventTypeMousePress, MouseButton: 1}); err != nil {
		t.Fatal(err)
	}
	if got := mw.mouse[6:12]; !bytes.Equal(got, []byte{0x01, 0xFF, 0x7F, 0x00, 0x00, 0x00}) {
		t.Fatalf("press report = % x, want 01 ff 7f 00 00 00", got)
	}

	// Scroll up 1 tick -> wheel +3
	if err := c.Handle(&HIDCommand{Event: EventTypeMouseScroll, MouseScroll: 1}); err != nil {
		t.Fatal(err)
	}
	if got := mw.mouse[12:18]; !bytes.Equal(got, []byte{0x01, 0xFF, 0x7F, 0x00, 0x00, 0x03}) {
		t.Fatalf("scroll up report = % x, want 01 ff 7f 00 00 03", got)
	}

	// Scroll down 1 tick -> wheel -3 (0xFD)
	if err := c.Handle(&HIDCommand{Event: EventTypeMouseScroll, MouseScroll: -1}); err != nil {
		t.Fatal(err)
	}
	if got := mw.mouse[18:24]; !bytes.Equal(got, []byte{0x01, 0xFF, 0x7F, 0x00, 0x00, 0xFD}) {
		t.Fatalf("scroll down report = % x, want 01 ff 7f 00 00 fd", got)
	}

	// Release all buttons
	if err := c.Handle(&HIDCommand{Event: EventTypeMouseRelease, MouseButton: 0}); err != nil {
		t.Fatal(err)
	}
	if got := mw.mouse[24:30]; !bytes.Equal(got, []byte{0x00, 0xFF, 0x7F, 0x00, 0x00, 0x00}) {
		t.Fatalf("release report = % x, want 00 ff 7f 00 00 00", got)
	}
}

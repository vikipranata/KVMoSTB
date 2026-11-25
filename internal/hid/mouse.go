package hid

/*
	Mouse state and report builder for the 6-byte absolute mouse report:

	    [buttons(3b)+pad, X16 0-32767 LE, Y16 0-32767 LE, wheel i8 relative]

	The web client emits 12-bit coordinates (0-4095) and the gadget uses 16-bit
	(0-32767), so coordinates are rescaled on the way in.
*/

// Mouse coordinate ranges.
const (
	WebCoordMax  = 4095  // 12-bit, as emitted by the web client
	GadgetMax    = 32767 // 16-bit, as advertised in the report descriptor
	MinGadgetPos = 0
)

// RescaleCoord maps a 12-bit web coordinate (0-4095) to the 16-bit gadget
// range (0-32767), clamping out-of-range input.
func RescaleCoord(v int) int {
	if v < 0 {
		return MinGadgetPos
	}
	if v > WebCoordMax {
		v = WebCoordMax
	}
	return v * GadgetMax / WebCoordMax
}

// Mouse is the current mouse state.
type Mouse struct {
	Buttons uint8 // bit0 left, bit1 right, bit2 middle
	X       int   // 0-32767
	Y       int   // 0-32767
}

// Report builds the current 6-byte mouse report with the given wheel step
// (positive = up, per the HID usage table for wheel).
func (m *Mouse) Report(wheel int8) [6]byte {
	var r [6]byte
	r[0] = m.Buttons & 0x07
	r[1] = byte(m.X)
	r[2] = byte(m.X >> 8)
	r[3] = byte(m.Y)
	r[4] = byte(m.Y >> 8)
	r[5] = byte(wheel)
	return r
}

// SyncButtons sets the button mask from a move event's mouse_move_button_state
// (JS and HID share the layout: bit0 left, bit1 right, bit2 middle). It returns
// true when the mask changed, so the caller sends a report even without movement.
func (m *Mouse) SyncButtons(jsMask uint8) bool {
	newButtons := jsMask & 0x07
	if newButtons == m.Buttons {
		return false
	}
	m.Buttons = newButtons
	return true
}

// MoveAbsolute sets the absolute position from 12-bit web coordinates.
// Returns true if the position changed.
func (m *Mouse) MoveAbsolute(x12, y12 int) bool {
	x, y := RescaleCoord(x12), RescaleCoord(y12)
	if x == m.X && y == m.Y {
		return false
	}
	m.X, m.Y = x, y
	return true
}

// Nudge moves by a relative delta, clamped to the gadget range. Used by the
// jiggler (the gadget has no relative mode). Returns true if it moved.
func (m *Mouse) Nudge(dx, dy int) bool {
	x, y := m.X+dx, m.Y+dy
	if x < MinGadgetPos {
		x = MinGadgetPos
	}
	if x > GadgetMax {
		x = GadgetMax
	}
	if y < MinGadgetPos {
		y = MinGadgetPos
	}
	if y > GadgetMax {
		y = GadgetMax
	}
	if x == m.X && y == m.Y {
		return false
	}
	m.X, m.Y = x, y
	return true
}

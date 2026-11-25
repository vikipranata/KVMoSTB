package hid

/*
	WebSocket command contract (JSON fields and event numbering) and shared types.
	Transport is the Writer interface.
*/

type EventType int

const (
	EventTypeKeyPress EventType = iota
	EventTypeKeyRelease
	EventTypeMouseMove
	EventTypeMousePress
	EventTypeMouseRelease
	EventTypeMouseScroll
	EventTypeHIDCommand
)

// HIDCommand is the JSON message the web UI sends over /ws/hid.
type HIDCommand struct {
	Event                EventType `json:"event"`
	Rid                  string    `json:"rid,omitempty"` // Reply ID; if set, the backend sends an ACK
	Keycode              int       `json:"keycode,omitempty"`
	IsRightModKey        bool      `json:"is_right_modifier_key,omitempty"`
	MouseAbsX            int       `json:"mouse_x,omitempty"` // Absolute position (web 12-bit 0-4095)
	MouseAbsY            int       `json:"mouse_y,omitempty"`
	MouseRelX            int       `json:"mouse_rel_x,omitempty"`
	MouseRelY            int       `json:"mouse_rel_y,omitempty"`
	MouseMoveButtonState int       `json:"mouse_move_button_state,omitempty"`
	MouseButton          int       `json:"mouse_button,omitempty"` // 1 left, 2 right, 3 middle
	MouseScroll          int       `json:"mouse_scroll,omitempty"` // Positive = up, negative = down
}

// Writer is the transport for raw HID reports: /dev/hidg0 for the keyboard and
// /dev/hidg1 for the mouse.
type Writer interface {
	WriteKeyboard(report [8]byte) error
	WriteMouse(report [6]byte) error
}

// Config holds the HID behaviour options.
type Config struct {
	// WheelSensitivity is the wheel step sent per scroll event (1-127).
	WheelSensitivity int8
	// InvertScrollDirection flips the wheel sign (for remote machines
	// that expect the opposite convention).
	InvertScrollDirection bool
}

type controllerConfig struct {
	WheelSensitivity      int8
	InvertScrollDirection bool
}

// Config normalizes zero values to sensible defaults.
func (c *Config) normalized() controllerConfig {
	ws := c.WheelSensitivity
	if ws < 1 || ws > 127 {
		ws = 3
	}
	return controllerConfig{
		WheelSensitivity:      ws,
		InvertScrollDirection: c.InvertScrollDirection,
	}
}

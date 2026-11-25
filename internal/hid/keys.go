package hid

/*
	JS legacy keycode to HID scancode table. Reports are the standard 8-byte
	boot keyboard report:
	    [modifier, reserved, 6 x scancode]
*/

const (
	MOD_LCTRL  = 0x01
	MOD_LSHIFT = 0x02
	MOD_LALT   = 0x04
	MOD_LGUI   = 0x08
	MOD_RCTRL  = 0x10
	MOD_RSHIFT = 0x20
	MOD_RALT   = 0x40
	MOD_RGUI   = 0x80
)

// IsModifierKey checks if the given JavaScript keycode corresponds to a
// modifier key.
func IsModifierKey(keycode uint8) bool {
	switch keycode {
	case 16, 17, 18, 91: // Shift, Control, Alt, Meta (Windows/Command)
		return true
	default:
		return false
	}
}

func modifierBitFor(keycode uint8, isRight bool) (uint8, bool) {
	var bit uint8
	switch keycode {
	case 17:
		bit = MOD_LCTRL
		if isRight {
			bit = MOD_RCTRL
		}
	case 16:
		bit = MOD_LSHIFT
		if isRight {
			bit = MOD_RSHIFT
		}
	case 18:
		bit = MOD_LALT
		if isRight {
			bit = MOD_RALT
		}
	case 91:
		bit = MOD_LGUI
		if isRight {
			bit = MOD_RGUI
		}
	default:
		return 0, false
	}
	return bit, true
}

// Keyboard is the held-key state plus the report builder.
type Keyboard struct {
	Modkey uint8    // modifier state
	Keys   [6]uint8 // pressed scancodes (rollover slots)
}

// Report builds the current 8-byte boot keyboard report.
func (k *Keyboard) Report() [8]byte {
	var r [8]byte
	r[0] = k.Modkey
	copy(r[2:], k.Keys[:])
	return r
}

// Press registers a key press (JS keycode) and returns the new report. Modifiers
// set the modifier byte; unsupported keys are ignored.
func (k *Keyboard) Press(jsKeycode uint8, isRight bool) [8]byte {
	if bit, ok := modifierBitFor(jsKeycode, isRight); ok {
		k.Modkey |= bit
		return k.Report()
	}

	sc := jsKeyCodeToHIDOpcode(jsKeycode)
	if sc == 0 {
		return k.Report()
	}

	// Already pressed? No state change.
	for i := 0; i < 6; i++ {
		if k.Keys[i] == sc {
			return k.Report()
		}
	}

	// Take the first free rollover slot.
	for i := 0; i < 6; i++ {
		if k.Keys[i] == 0x00 {
			k.Keys[i] = sc
			return k.Report()
		}
	}

	// No space left: drop the oldest (slot 0) so the keypress lands.
	// Six slots is the boot-protocol limit; N-key rollover needs a full
	// keyboard report descriptor.
	k.Keys[0] = sc
	return k.Report()
}

// Release clears a key (JS keycode) and returns the new report.
func (k *Keyboard) Release(jsKeycode uint8, isRight bool) [8]byte {
	if bit, ok := modifierBitFor(jsKeycode, isRight); ok {
		k.Modkey &^= bit
		return k.Report()
	}

	sc := jsKeyCodeToHIDOpcode(jsKeycode)
	if sc == 0 {
		return k.Report()
	}

	for i := 0; i < 6; i++ {
		if k.Keys[i] == sc {
			k.Keys[i] = 0x00
			return k.Report()
		}
	}
	return k.Report()
}

// ReleaseAll returns all keys and modifiers to the idle state.
func (k *Keyboard) ReleaseAll() [8]byte {
	k.Modkey = 0
	k.Keys = [6]uint8{}
	return k.Report()
}

// jsKeyCodeToHIDOpcode converts a JavaScript keyCode to a HID usage id.
// Returns 0 for unsupported keys.
func jsKeyCodeToHIDOpcode(keycode uint8) uint8 {
	// Letters A-Z
	if keycode >= 65 && keycode <= 90 {
		return (keycode - 65) + 0x04 // 'A' is 0x04
	}

	// Numbers 1-9 (top row, not numpad)
	if keycode >= 49 && keycode <= 57 {
		return (keycode - 49) + 0x1E // '1' is 0x1E
	}

	// F1 to F12
	if keycode >= 112 && keycode <= 123 {
		return (keycode - 112) + 0x3A // 'F1' is 0x3A
	}

	switch keycode {
	case 8:
		return 0x2A // Backspace
	case 9:
		return 0x2B // Tab
	case 13:
		return 0x28 // Enter
	case 16:
		return 0xE1 // Left shift
	case 17:
		return 0xE0 // Left Ctrl
	case 18:
		return 0xE2 // Left Alt
	case 19:
		return 0x48 // Pause
	case 20:
		return 0x39 // Caps Lock
	case 27:
		return 0x29 // Escape
	case 32:
		return 0x2C // Spacebar
	case 33:
		return 0x4B // Page Up
	case 34:
		return 0x4E // Page Down
	case 35:
		return 0x4D // End
	case 36:
		return 0x4A // Home
	case 37:
		return 0x50 // Left Arrow
	case 38:
		return 0x52 // Up Arrow
	case 39:
		return 0x4F // Right Arrow
	case 40:
		return 0x51 // Down Arrow
	case 44:
		return 0x46 // Print Screen or F13 (Firefox)
	case 45:
		return 0x49 // Insert
	case 46:
		return 0x4C // Delete
	case 48:
		return 0x27 // 0 (not Numpads)
	case 59:
		return 0x33 // ';'
	case 61:
		return 0x2E // '='
	case 91:
		return 0xE3 // Left GUI (Windows)
	case 92:
		return 0xE7 // Right GUI
	case 93:
		return 0x65 // Menu key
	case 96:
		return 0x62 // 0 (Numpads)
	case 97:
		return 0x59 // 1 (Numpads)
	case 98:
		return 0x5A // 2 (Numpads)
	case 99:
		return 0x5B // 3 (Numpads)
	case 100:
		return 0x5C // 4 (Numpads)
	case 101:
		return 0x5D // 5 (Numpads)
	case 102:
		return 0x5E // 6 (Numpads)
	case 103:
		return 0x5F // 7 (Numpads)
	case 104:
		return 0x60 // 8 (Numpads)
	case 105:
		return 0x61 // 9 (Numpads)
	case 106:
		return 0x55 // * (Numpads)
	case 107:
		return 0x57 // + (Numpads)
	case 109:
		return 0x56 // - (Numpads)
	case 110:
		return 0x63 // dot (Numpads)
	case 111:
		return 0x54 // divide (Numpads)
	case 144:
		return 0x53 // Num Lock
	case 145:
		return 0x47 // Scroll Lock
	case 146:
		return 0x58 // Numpad enter
	case 173:
		return 0x2D // -
	case 186:
		return 0x33 // ';'
	case 187:
		return 0x2E // '='
	case 188:
		return 0x36 // ','
	case 189:
		return 0x2D // '-'
	case 190:
		return 0x37 // '.'
	case 191:
		return 0x38 // '/'
	case 192:
		return 0x35 // '`'
	case 219:
		return 0x2F // '['
	case 220:
		return 0x31 // backslash
	case 221:
		return 0x30 // ']'
	case 222:
		return 0x34 // '\''
	default:
		return 0x00 // Unknown / unsupported
	}
}

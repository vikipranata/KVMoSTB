package hid

/*
	Controller: input state machine and command dispatch for the USB HID gadget.
	Reports go through the Writer interface; the jiggler nudges the absolute
	position instead of sending relative deltas.
*/

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	jigglerIdleTimeout = 30 * time.Second
	jigglerMoveAmount  = 20 // gadget units (of 32767) to nudge
)

type jigglerState struct {
	mu      sync.Mutex
	enabled bool
	stopCh  chan struct{}
}

// Controller owns the keyboard/mouse state and ships reports to the Writer.
type Controller struct {
	cfg              controllerConfig
	writer           Writer
	KB               Keyboard
	Mouse            Mouse
	activityMu       sync.Mutex
	lastActivityTime time.Time

	jiggler jigglerState
}

// NewController creates a controller writing reports to w. A nil config
// selects the defaults.
func NewController(cfg *Config, w Writer) *Controller {
	if cfg == nil {
		cfg = &Config{}
	}
	return &Controller{
		cfg:              cfg.normalized(),
		writer:           w,
		lastActivityTime: time.Now(),
	}
}

// Handle dispatches one WS command (HIDCommand) to the HID state machine
// and writes the resulting report(s).
func (c *Controller) Handle(cmd *HIDCommand) error {
	if cmd == nil {
		return errors.New("nil command")
	}
	c.RecordActivity()

	switch cmd.Event {
	case EventTypeKeyPress:
		return c.KeyPress(uint8(cmd.Keycode), cmd.IsRightModKey)
	case EventTypeKeyRelease:
		return c.KeyRelease(uint8(cmd.Keycode), cmd.IsRightModKey)
	case EventTypeMouseMove:
		// Every move re-syncs the button mask: an absolute click is a move frame
		// carrying a button.
		buttonsDirty := c.Mouse.SyncButtons(uint8(cmd.MouseMoveButtonState))
		// Zero coordinates mean unset, so the exact top-left corner is reachable
		// only as part of a move that leaves it.
		if cmd.MouseAbsX != 0 || cmd.MouseAbsY != 0 {
			return c.MouseMoveAbsolute(cmd.MouseAbsX, cmd.MouseAbsY, buttonsDirty)
		}
		return c.MouseNudge(cmd.MouseRelX, cmd.MouseRelY)
	case EventTypeMousePress:
		return c.MouseButtonPress(uint8(cmd.MouseButton))
	case EventTypeMouseRelease:
		return c.MouseButtonRelease(uint8(cmd.MouseButton))
	case EventTypeMouseScroll:
		return c.MouseScroll(cmd.MouseScroll)
	default:
		return fmt.Errorf("unsupported HID event %d", cmd.Event)
	}
}

func (c *Controller) KeyPress(jsKeycode uint8, isRight bool) error {
	report := c.KB.Press(jsKeycode, isRight)
	return c.writer.WriteKeyboard(report)
}

func (c *Controller) KeyRelease(jsKeycode uint8, isRight bool) error {
	report := c.KB.Release(jsKeycode, isRight)
	return c.writer.WriteKeyboard(report)
}

// MouseMoveAbsolute moves to an absolute position (12-bit web coordinates).
// buttonsDirty forces a report at an unchanged position: a stationary click is
// a move frame with a new button mask, and skipping it would lose the click.
func (c *Controller) MouseMoveAbsolute(x12, y12 int, buttonsDirty bool) error {
	if !c.Mouse.MoveAbsolute(x12, y12) && !buttonsDirty {
		return nil // no movement and no button change, nothing to send
	}
	return c.writer.WriteMouse(c.Mouse.Report(0))
}

// MouseNudge moves by a relative delta (clamped, absolute gadget).
func (c *Controller) MouseNudge(dx, dy int) error {
	if !c.Mouse.Nudge(dx, dy) {
		return nil
	}
	return c.writer.WriteMouse(c.Mouse.Report(0))
}

// MouseButtonPress presses a button: 1 left, 2 right, 3 middle, 0 release all.
func (c *Controller) MouseButtonPress(button uint8) error {
	switch button {
	case 1:
		c.Mouse.Buttons |= 0x01
	case 2:
		c.Mouse.Buttons |= 0x02
	case 3:
		c.Mouse.Buttons |= 0x04
	default:
		return fmt.Errorf("invalid mouse button %d", button)
	}
	return c.writer.WriteMouse(c.Mouse.Report(0))
}

// MouseButtonRelease releases a button: 1 left, 2 right, 3 middle, 0 all.
func (c *Controller) MouseButtonRelease(button uint8) error {
	switch button {
	case 0:
		c.Mouse.Buttons = 0x00
	case 1:
		c.Mouse.Buttons &^= 0x01
	case 2:
		c.Mouse.Buttons &^= 0x02
	case 3:
		c.Mouse.Buttons &^= 0x04
	default:
		return fmt.Errorf("invalid mouse button %d", button)
	}
	return c.writer.WriteMouse(c.Mouse.Report(0))
}

// MouseScroll scrolls: positive tilt = up (HID wheel positive = up).
func (c *Controller) MouseScroll(tilt int) error {
	if tilt == 0 {
		return nil
	}
	step := c.cfg.WheelSensitivity
	if c.cfg.InvertScrollDirection {
		step = -step
	}
	var wheel int8
	if tilt > 0 {
		wheel = step
	} else {
		wheel = -step
	}
	return c.writer.WriteMouse(c.Mouse.Report(wheel))
}

// ReleaseAll ships idle keyboard + mouse reports (stuck-key recovery).
func (c *Controller) ReleaseAll() error {
	if err := c.writer.WriteKeyboard(c.KB.ReleaseAll()); err != nil {
		return err
	}
	c.Mouse.Buttons = 0
	return c.writer.WriteMouse(c.Mouse.Report(0))
}

// StartJiggler moves the pointer a few units every jigglerIdleTimeout while
// idle, to keep the remote session from sleeping.
func (c *Controller) StartJiggler() {
	c.jiggler.mu.Lock()
	defer c.jiggler.mu.Unlock()
	if c.jiggler.enabled {
		return
	}
	c.jiggler.enabled = true
	c.jiggler.stopCh = make(chan struct{})
	stopCh := c.jiggler.stopCh

	go func() {
		nudgeRight := true
		ticker := time.NewTicker(jigglerIdleTimeout)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				if time.Since(c.getLastActivityTime()) < jigglerIdleTimeout {
					continue
				}
				dx := jigglerMoveAmount
				if !nudgeRight {
					dx = -jigglerMoveAmount
				}
				_ = c.MouseNudge(dx, 0)
				nudgeRight = !nudgeRight
			}
		}
	}()
}

// StopJiggler disables the jiggler.
func (c *Controller) StopJiggler() {
	c.jiggler.mu.Lock()
	defer c.jiggler.mu.Unlock()
	if !c.jiggler.enabled {
		return
	}
	c.jiggler.enabled = false
	close(c.jiggler.stopCh)
}

// IsJigglerEnabled reports whether the jiggler is running.
func (c *Controller) IsJigglerEnabled() bool {
	c.jiggler.mu.Lock()
	defer c.jiggler.mu.Unlock()
	return c.jiggler.enabled
}

// RecordActivity resets the idle timer.
func (c *Controller) RecordActivity() {
	c.activityMu.Lock()
	c.lastActivityTime = time.Now()
	c.activityMu.Unlock()
}

func (c *Controller) getLastActivityTime() time.Time {
	c.activityMu.Lock()
	defer c.activityMu.Unlock()
	return c.lastActivityTime
}

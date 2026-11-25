package gadget

/*
	HIDWriter ships raw reports to the gadget's /dev/hidg files.

	A write to /dev/hidgN blocks until the host polls that interrupt endpoint,
	and a BIOS/UEFI screen or a host with no bound HID driver can leave an
	endpoint unpolled indefinitely. Each device therefore gets its own writer
	goroutine and a bounded queue: WriteKeyboard and WriteMouse enqueue and
	return at once, a blocked write stalls only its own queue, and a full queue
	drops the oldest report so fresh input never waits behind stale events.
	Held-key state lives in hid.Controller, so a dropped report costs at most
	one stale position; ReleaseAll unwedges it.

	Handles that went stale after a UDC rebind are detected by inode comparison
	and reopened. A device-gone write error reopens and retries once.
*/

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"time"

	"kvmostb/internal/logging"
)

type HIDWriter struct {
	G         *Gadget
	kbdNode   string
	mouseNode string
	kbdCh     chan [8]byte
	mouseCh   chan [6]byte
	startOnce sync.Once
	logMu     sync.Mutex
	lastLog   time.Time
}

// NewHIDWriter builds the async writer. The per-device writer
// goroutines start lazily on the first report.
func NewHIDWriter(g *Gadget) *HIDWriter {
	return &HIDWriter{
		G:         g,
		kbdNode:   "/dev/hidg0",
		mouseNode: "/dev/hidg1",
		kbdCh:     make(chan [8]byte, 16),
		mouseCh:   make(chan [6]byte, 16),
	}
}

func (w *HIDWriter) start() {
	w.startOnce.Do(func() {
		go func() {
			for r := range w.kbdCh {
				w.write("keyboard", w.G.KeyboardFile, w.kbdNode, r[:])
			}
		}()
		go func() {
			for r := range w.mouseCh {
				w.write("mouse", w.G.MouseFile, w.mouseNode, r[:])
			}
		}()
	})
}

// WriteKeyboard implements hid.Writer. It queues the report and returns at once,
// so callers never block on an unpolled endpoint.
func (w *HIDWriter) WriteKeyboard(r [8]byte) error {
	w.start()
	select {
	case w.kbdCh <- r:
	default:
		<-w.kbdCh // drop the oldest, fresh input wins
		w.warnRatef("HID keyboard queue full; dropped oldest report")
		w.kbdCh <- r
	}
	return nil
}

// WriteMouse implements hid.Writer (see WriteKeyboard).
func (w *HIDWriter) WriteMouse(r [6]byte) error {
	w.start()
	select {
	case w.mouseCh <- r:
	default:
		<-w.mouseCh
		w.warnRatef("HID mouse queue full; dropped oldest report")
		w.mouseCh <- r
	}
	return nil
}

// write ships one report on the device's own goroutine, the only place a
// /dev/hidg write may block.
func (w *HIDWriter) write(name string, getFile func() *os.File, node string, r []byte) {
	f := getFile()
	if f == nil {
		w.warnRatef("HID %s: handle not open", name)
		return
	}
	if nodeChanged(f, node) {
		if err := w.G.Open(); err != nil {
			w.warnRatef("HID %s: reopen hidg: %v", name, err)
			return
		}
		f = getFile()
		if f == nil {
			w.warnRatef("HID %s: handle not open after reopen", name)
			return
		}
	}
	t0 := time.Now()
	if _, err := f.Write(r); err != nil {
		if deviceGone(err) {
			if err2 := w.G.Open(); err2 != nil {
				w.warnRatef("HID %s: reopen hidg: %v", name, err2)
				return
			}
			f = getFile()
			if f == nil {
				return
			}
			if _, err2 := f.Write(r); err2 != nil {
				w.warnRatef("HID %s: retry write: %v", name, err2)
				return
			}
		} else {
			w.warnRatef("HID %s: write: %v", name, err)
			return
		}
	} else if dt := time.Since(t0); dt > 2*time.Second {
		w.warnRatef("HID %s: write took %v (host not polling this endpoint? input on this device is delayed)", name, dt)
	}
}

// warnRatef logs at most once per second so a wedged device cannot flood the journal.
func (w *HIDWriter) warnRatef(format string, args ...any) {
	w.logMu.Lock()
	defer w.logMu.Unlock()
	if time.Since(w.lastLog) < time.Second {
		return
	}
	w.lastLog = time.Now()
	logging.Warnf(format, args...)
}

func deviceGone(err error) bool {
	return errors.Is(err, os.ErrClosed) ||
		errors.Is(err, syscall.ENODEV) ||
		errors.Is(err, syscall.EBADF) ||
		errors.Is(err, syscall.ENXIO)
}

// nodeChanged reports whether the path now points at a different inode than the
// open file. A UDC rebind recreates /dev/hidg* while old fds keep accepting
// writes, so only the inode check detects the stale handle. A missing path
// counts as unchanged: it surfaces as ENXIO or ENODEV on write and takes the
// reopen path.
func nodeChanged(f *os.File, path string) bool {
	var st, ft syscall.Stat_t
	if syscall.Fstat(int(f.Fd()), &ft) != nil {
		return true // cannot inspect the fd; treat as stale
	}
	if syscall.Stat(path, &st) != nil {
		return false
	}
	return st.Ino != ft.Ino
}

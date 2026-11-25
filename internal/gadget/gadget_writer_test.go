package gadget

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNodeChangedDetectsRecreation proves the stale-hidg detector: after
// the path is atomically replaced by a new inode (what a UDC rebind does
// to /dev/hidg0,1), the old fd must be flagged as changed and a fresh fd
// must not.
func TestNodeChangedDetectsRecreation(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/hidg0"
	p2 := dir + "/hidg0.new"

	f1, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := os.Create(p2)
	if err != nil {
		t.Fatal(err)
	}
	f2.Close()

	// Atomic replace = new inode at the same path (UDC rebind equivalent).
	if err := os.Rename(p2, p); err != nil {
		t.Fatal(err)
	}
	if !nodeChanged(f1, p) {
		t.Fatal("stale fd after node recreation: nodeChanged must be true")
	}
	f1.Close()

	f3, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f3.Close()
	if nodeChanged(f3, p) {
		t.Fatal("fresh fd: nodeChanged must be false")
	}
}

// TestWriterBlockedKeyboardDoesNotStarveMouse covers a /dev/hidg write that
// blocks until the host polls the endpoint. Here the "keyboard device"
// is a TCP fd whose peer never reads (write blocks once the buffers
// fill - same shape as a wedged /dev/hidg0), and the mouse device is a
// plain file. Requirements checked:
//  1. while the keyboard write is blocked, mouse reports still land;
//  2. enqueue never blocks (the report must not hang /api/hid/reset);
//  3. once the peer reads again, queued reports flow in order and the
//     most recent one survives drop-oldest.
func TestWriterBlockedKeyboardDoesNotStarveMouse(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var kbd net.Conn
	select {
	case kbd = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("no accept")
	}
	defer kbd.Close()
	tcpClient, ok := client.(*net.TCPConn)
	if !ok {
		t.Fatal("expected *net.TCPConn")
	}
	kf, err := tcpClient.File()
	if err != nil {
		t.Fatal(err)
	}
	defer kf.Close()

	mp := filepath.Join(t.TempDir(), "mouse")
	mf, err := os.Create(mp)
	if err != nil {
		t.Fatal(err)
	}
	defer mf.Close()

	g := &Gadget{keyboard: kf, mouse: mf}
	w := NewHIDWriter(g)

	// Flood the keyboard until its writer goroutine blocks on the full
	// TCP buffers.
	const flood = 20000
	for i := 0; i < flood; i++ {
		w.WriteKeyboard([8]byte{1, byte(i % 251)})
	}
	time.Sleep(300 * time.Millisecond) // let the goroutine hit the wall

	// 1+2: mouse must still be delivered promptly, enqueue must not hang.
	start := time.Now()
	if err := w.WriteMouse([6]byte{0, 2}); err != nil {
		t.Fatal(err)
	}
	var size int64 = -1
	for time.Since(start) < 2*time.Second {
		st, err := mf.Stat()
		if err == nil {
			size = st.Size()
			if size > 0 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if size <= 0 {
		t.Fatal("mouse report starved while keyboard write was blocked")
	}
	if dt := time.Since(start); dt > time.Second {
		t.Fatalf("mouse delivery took %v while keyboard was blocked", dt)
	}

	// 3: drain the peer; queued reports flow in order, latest survives.
	kbd.SetReadDeadline(time.Now().Add(5 * time.Second))
	var got, last int
	buf := make([]byte, 8)
	for {
		n, err := kbd.Read(buf)
		if n >= 8 {
			got++
			last = int(buf[1])
		}
		if err != nil {
			break
		}
	}
	if got < 16 {
		t.Fatalf("only %d queued keyboard reports delivered after drain, want >= 16", got)
	}
	if last != (flood-1)%251 {
		t.Fatalf("last delivered report = %d, want %d (most recent must survive drop-oldest)", last, (flood-1)%251)
	}
}

// TestOpenConcurrentWithAccessors guards the fileMu contract:
// Open/close of the handles may run concurrently with the accessors
// and with each other without deadlocking.
func TestOpenConcurrentWithAccessors(t *testing.T) {
	dir := t.TempDir()
	kp := filepath.Join(dir, "kbd")
	mp := filepath.Join(dir, "mouse")
	for _, pth := range []string{kp, mp} {
		f, err := os.OpenFile(pth, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	g := &Gadget{kbdNode: kp, mouseNode: mp}
	if err := g.Open(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			if err := g.Open(); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 200; i++ {
		_ = g.KeyboardFile()
		_ = g.MouseFile()
		if i == 100 {
			_ = g.Shutdown()
		}
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: concurrent Open + accessors never finished")
	}
}

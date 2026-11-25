package gadget

/*
	USB gadget bring-up for the HG680-FJ dwc2 OTG port through configfs.

	The gadget exposes a boot keyboard (8-byte report), an absolute mouse
	(6-byte report) and, when an ISO is attached, a mass-storage CD-ROM.
	Ordering matters: the role switch arms the PHY first, and each report
	descriptor is written before the UDC is bound.
*/

import (
	"bytes"
	"fmt"
	"kvmostb/internal/logging"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	Name         = "kvmostb"
	VendorID     = "0x1d6b"
	ProductID    = "0x0104"
	BCDDevice    = "0x0100"
	BCDUSB       = "0x0200"
	MaxPowerMA   = "192"
	Manufacturer = "KVMoSTB"
	ProductName  = "KVMoSTB KVM Gadget"
	SerialNumber = "kvmostb0001"
	ConfigLabel  = "HID keyboard+mouse, ISO mass storage"

	// Kernel 6.12 configfs strings are grouped per language ID.
	stringsLangID = "0x409"

	KeyboardFunc = "hid.usb0"
	MouseFunc    = "hid.usb1"
	StorageFunc  = "mass_storage.usb0"
	ConfigName   = "c.1"

	configRoot = "/sys/kernel/config/usb_gadget"
	udcClass   = "/sys/class/udc"
)

// HID report descriptors. Every Input/Output item needs an explicit Report Size
// (0x75); without it the host parses 0-bit fields and each report fails with -EOVERFLOW.
var (
	// Boot keyboard: [modifier, reserved, 6 x scancode]  in=8B  out=1B (LEDs)
	keyboardReportDesc = []byte{
		0x05, 0x01, 0x09, 0x06, 0xa1, 0x01, 0x05, 0x07, 0x19, 0xe0, 0x29, 0xe7,
		0x15, 0x00, 0x25, 0x01, 0x75, 0x01, 0x95, 0x08, 0x81, 0x02, // modifiers
		0x95, 0x01, 0x75, 0x08, 0x81, 0x01, // reserved
		0x95, 0x05, 0x75, 0x01, 0x05, 0x08, 0x19, 0x01, 0x29, 0x05, 0x91, 0x02, // LEDs
		0x95, 0x01, 0x75, 0x03, 0x91, 0x01, // LED padding
		0x95, 0x06, 0x75, 0x08, 0x15, 0x00, 0x26, 0x63, 0x00, 0x05, 0x07,
		0x19, 0x00, 0x29, 0x63, 0x81, 0x00, 0xc0, // keys
	}
	// Absolute mouse: [buttons(3b)+5b pad, X16 0-32767, Y16 0-32767, wheel i8]  in=6B
	mouseReportDesc = []byte{
		0x05, 0x01, 0x09, 0x02, 0xa1, 0x01, 0x09, 0x01, 0xa1, 0x00, 0x05, 0x09,
		0x19, 0x01, 0x29, 0x03, 0x15, 0x00, 0x25, 0x01, 0x75, 0x01, 0x95, 0x03,
		0x81, 0x02, 0x75, 0x05, 0x95, 0x01, 0x81, 0x03, // buttons + pad
		0x05, 0x01, 0x09, 0x30, 0x09, 0x31, 0x16, 0x00, 0x00, 0x26, 0xff, 0x7f,
		0x75, 0x10, 0x95, 0x02, 0x81, 0x02, // X, Y
		0x09, 0x38, 0x15, 0x81, 0x25, 0x7f, 0x75, 0x08, 0x95, 0x01,
		0x81, 0x06, 0xc0, 0xc0, // wheel
	}
)

// Options configures the gadget bring-up.
type Options struct {
	// ISOFile is the backing file for the mass-storage function.
	// Empty = no mass-storage function (added later via SetISOFile).
	ISOFile string
	// UDCName overrides auto-detection (normally the single entry in
	// /sys/class/udc, e.g. "ff400000.usb").
	UDCName string
}

// Gadget is a live USB gadget configuration.
type Gadget struct {
	path     string
	udc      string
	isoFile  string
	keyboard *os.File
	mouse    *os.File
	// fileMu guards the hidg handles against the async writer goroutines.
	fileMu sync.Mutex
	// kbdNode and mouseNode override /dev/hidg0 and /dev/hidg1; tests point them at temp files.
	kbdNode   string
	mouseNode string
}

// setRoleDevice arms the shared USB2 PHY for device mode. dr_mode on the dwc2 node
// does not, so without it the link attaches but never reaches "configured".
// The attribute accepts only none, host and device; failures are non-fatal.
func setRoleDevice() {
	matches, _ := filepath.Glob("/sys/class/usb_role/*/role")
	for _, p := range matches {
		if err := os.WriteFile(p, []byte("device"), 0o644); err != nil {
			logging.Debugf("role switch %s: %v (continuing)", p, err)
		} else {
			logging.Debugf("role switch %s set to device", p)
		}
	}
}

// Init brings the gadget up idempotently: an existing gadget is torn down and
// rebuilt, so every daemon start and the oneshot unit can call it.
func Init(opts Options) (*Gadget, error) {
	loadModules()
	setRoleDevice()

	gadgetRoot := filepath.Join(configRoot, Name)
	logging.Debugf("init: existing state present=%v; cleaning %s", dirExists(gadgetRoot), gadgetRoot)
	if err := cleanup(gadgetRoot); err != nil {
		return nil, fmt.Errorf("gadget cleanup: %w", err)
	}

	udc, err := pickUDC(opts.UDCName)
	if err != nil {
		logging.Errorf("UDC pick failed: %v", err)
		return nil, err
	}

	g := &Gadget{path: gadgetRoot, udc: udc}
	if err := g.build(opts); err != nil {
		_ = cleanup(gadgetRoot)
		return nil, err
	}

	if err := g.bindUDC(); err != nil {
		logging.Errorf("UDC bind failed: %v", err)
		_ = cleanup(gadgetRoot)
		return nil, fmt.Errorf("bind UDC %s: %w", udc, err)
	}

	logState, _ := os.ReadFile(filepath.Join(udcClass, udc, "state"))
	logging.Infof("gadget %s up on UDC %s (state=%s)", Name, udc, strings.TrimSpace(string(logState)))
	return g, nil
}

// build creates the configfs tree. report_desc is written before the UDC
// bind, as required by the configfs HID function.
func (g *Gadget) build(opts Options) error {
	mk := func(rel string) (string, error) {
		p := filepath.Join(g.path, rel)
		if err := os.MkdirAll(p, 0755); err != nil {
			return "", err
		}
		return p, nil
	}
	w := func(rel, value string) error {
		if err := os.WriteFile(filepath.Join(g.path, rel), []byte(value), 0644); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
		return nil
	}
	cfgDir := filepath.Join(g.path, "configs", ConfigName)
	link := func(funcName string) error {
		// configfs wants a symlink with just the function name
		_ = os.Remove(filepath.Join(cfgDir, funcName))
		if err := os.Symlink(filepath.Join(g.path, "functions", funcName),
			filepath.Join(cfgDir, funcName)); err != nil {
			return fmt.Errorf("link %s: %w", funcName, err)
		}
		return nil
	}

	if err := os.MkdirAll(g.path, 0755); err != nil {
		return err
	}
	for rel, value := range map[string]string{
		"idVendor":  VendorID,
		"idProduct": ProductID,
		"bcdDevice": BCDDevice,
		"bcdUSB":    BCDUSB,
	} {
		if err := w(rel, value); err != nil {
			return err
		}
	}

	lang := stringsLangID
	if _, err := mk("strings/" + lang); err != nil {
		return err
	}
	for _, s := range []struct{ rel, value string }{
		{"strings/" + lang + "/manufacturer", Manufacturer},
		{"strings/" + lang + "/product", ProductName},
		{"strings/" + lang + "/serialnumber", SerialNumber},
	} {
		if err := w(s.rel, s.value); err != nil {
			return err
		}
	}

	if _, err := mk("configs/" + ConfigName); err != nil {
		return err
	}
	if _, err := mk("configs/" + ConfigName + "/strings/" + lang); err != nil {
		return err
	}
	if err := w("configs/"+ConfigName+"/strings/"+lang+"/configuration", ConfigLabel); err != nil {
		return err
	}
	if err := w("configs/"+ConfigName+"/MaxPower", MaxPowerMA); err != nil {
		return err
	}

	// HID keyboard (boot interface)
	kbDir, err := mk("functions/" + KeyboardFunc)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(kbDir, "report_desc"), keyboardReportDesc, 0644); err != nil {
		return fmt.Errorf("keyboard report_desc: %w", err)
	}
	for rel, value := range map[string]string{
		"functions/" + KeyboardFunc + "/report_length": "8",
		"functions/" + KeyboardFunc + "/subclass":      "1", // boot subclass
		"functions/" + KeyboardFunc + "/protocol":      "1", // boot keyboard
	} {
		if err := w(rel, value); err != nil {
			return err
		}
	}

	// HID absolute mouse (boot interface)
	msDir, err := mk("functions/" + MouseFunc)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(msDir, "report_desc"), mouseReportDesc, 0644); err != nil {
		return fmt.Errorf("mouse report_desc: %w", err)
	}
	for rel, value := range map[string]string{
		"functions/" + MouseFunc + "/report_length": "6",
		"functions/" + MouseFunc + "/subclass":      "1",
		"functions/" + MouseFunc + "/protocol":      "2", // boot mouse
	} {
		if err := w(rel, value); err != nil {
			return err
		}
	}

	if err := link(KeyboardFunc); err != nil {
		return err
	}
	if err := link(MouseFunc); err != nil {
		return err
	}

	if opts.ISOFile == "" {
		logging.Debugf("build: no ISO at init (mount later via /api/iso/mount)")
	}
	// Optional mass storage (ISO)
	if opts.ISOFile != "" {
		if err := g.attachStorage(opts.ISOFile); err != nil {
			return err
		}
		g.isoFile = opts.ISOFile
	}

	return nil
}

// attachStorage adds the mass-storage function backing path.
func (g *Gadget) attachStorage(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("iso file: %w", err)
	}
	// On 6.12 the LUN attributes live in functions/mass_storage.usb0/lun.0/
	// (auto-created group); the function root only carries "stall".
	funcDir := filepath.Join(g.path, "functions", StorageFunc)
	if err := os.MkdirAll(funcDir, 0755); err != nil {
		return err
	}
	lunDir := filepath.Join(funcDir, "lun.0")
	if err := os.MkdirAll(lunDir, 0755); err != nil {
		return fmt.Errorf("mass_storage lun.0: %w", err)
	}
	logging.Debugf("attachStorage: %s (cfg dir exists=%v)", path, dirExists(filepath.Join(g.path, "configs", ConfigName)))
	// Order matters: writing "file" activates the LUN in the kernel, and
	// "ro"/"cdrom" then fail with -EBUSY. Flags first, "file" last.
	for _, kv := range [][2]string{
		{"ro", "1"},
		{"cdrom", "1"},
		{"removable", "1"},
		{"file", path},
	} {
		logging.Debugf("attachStorage: write lun.0/%s = %s", kv[0], kv[1])
		if err := os.WriteFile(filepath.Join(lunDir, kv[0]), []byte(kv[1]), 0644); err != nil {
			return fmt.Errorf("mass_storage lun.0/%s: %w", kv[0], err)
		}
	}
	_ = os.Remove(filepath.Join(g.path, "configs", ConfigName, StorageFunc))
	if err := os.Symlink(funcDir, filepath.Join(g.path, "configs", ConfigName, StorageFunc)); err != nil {
		return fmt.Errorf("link %s: %w", StorageFunc, err)
	}
	logging.Debugf("attachStorage: linked configs/%s/%s", ConfigName, StorageFunc)
	return nil
}

// SetISOFile swaps the mass-storage backing file, or removes the function when
// path is empty. The UDC rebinds, so the target re-enumerates the CD-ROM.
func (g *Gadget) SetISOFile(path string) error {
	logging.Infof("SetISOFile: %s -> %s", orNone(g.isoFile), orNone(path))
	if err := g.unbindUDC(); err != nil {
		// The unbind write can fail after the UDC was released, so check the real
		// state: a still-bound UDC makes adding a function fail with EINVAL.
		if b, rerr := os.ReadFile(filepath.Join(g.path, "UDC")); rerr == nil && len(bytes.TrimSpace(b)) > 0 {
			return fmt.Errorf("UDC still bound, cannot swap ISO (retry in a few seconds): %w", err)
		}
		logging.Warnf("UDC unbind write: %v (released anyway)", err)
	}

	// The storage function must be destroyed, not re-pointed: after the first bind
	// usb_f_mass_storage latches "enabled" and rejects "file" writes with EPERM.
	logging.Debugf("SetISOFile: UDC unbound; configs/%s exists=%v functions/%s exists=%v", ConfigName, dirExists(filepath.Join(g.path, "configs", ConfigName)), StorageFunc, dirExists(filepath.Join(g.path, "functions", StorageFunc)))
	if old := filepath.Join(g.path, "configs", ConfigName, StorageFunc); fileExists(old) {
		if err := os.Remove(old); err != nil {
			return fmt.Errorf("detach storage link: %w", err)
		}
	}
	if dir := filepath.Join(g.path, "functions", StorageFunc); dirExists(dir) {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove storage function: %w", err)
		}
	}
	g.isoFile = ""

	if path != "" {
		if err := g.attachStorage(path); err != nil {
			_ = g.bindUDC() // leave the gadget bound even if the swap failed
			return err
		}
		g.isoFile = path
	}

	if err := g.bindUDC(); err != nil {
		logging.Errorf("SetISOFile: rebind failed: %v", err)
		return err
	}
	// The rebind recreates /dev/hidg*; old handles keep accepting writes silently,
	// so reopen them.
	if err := g.Open(); err != nil {
		logging.Errorf("SetISOFile: reopen hidg handles: %v", err)
		return err
	}
	logging.Infof("SetISOFile done: iso=%s udc=%s state=%s", orNone(path), g.udc, g.UDCState())
	return nil
}

// open opens the keyboard and mouse hidg nodes, retrying while they appear after
// the UDC bind. Held handles are closed first, so it doubles as the reopen after a rebind.
func (g *Gadget) open() error {
	g.fileMu.Lock()
	defer g.fileMu.Unlock()
	if g.keyboard != nil {
		g.keyboard.Close()
		g.keyboard = nil
	}
	if g.mouse != nil {
		g.mouse.Close()
		g.mouse = nil
	}
	kbdNode, mouseNode := g.kbdNode, g.mouseNode
	if kbdNode == "" {
		kbdNode, mouseNode = "/dev/hidg0", "/dev/hidg1"
	}
	var kb, mouse *os.File
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		kb, err = os.OpenFile(kbdNode, os.O_RDWR, 0)
		if err == nil {
			mouse, err = os.OpenFile(mouseNode, os.O_RDWR, 0)
		}
		if err == nil {
			g.keyboard, g.mouse = kb, mouse
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("open hidg devices: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Open (re)opens both /dev/hidg handles.
func (g *Gadget) Open() error { return g.open() }

// KeyboardFile returns the open /dev/hidg0 handle.
func (g *Gadget) KeyboardFile() *os.File {
	g.fileMu.Lock()
	defer g.fileMu.Unlock()
	return g.keyboard
}

// MouseFile returns the open /dev/hidg1 handle.
func (g *Gadget) MouseFile() *os.File {
	g.fileMu.Lock()
	defer g.fileMu.Unlock()
	return g.mouse
}

// ISOFile returns the current mass-storage backing file ("" = none).
func (g *Gadget) ISOFile() string { return g.isoFile }

// UDCState returns the current UDC state string (e.g. "configured").
func (g *Gadget) UDCState() string {
	b, err := os.ReadFile(filepath.Join(g.path, "UDC"))
	if err != nil {
		return "error"
	}
	state, _ := os.ReadFile(filepath.Join(udcClass, g.udc, "state"))
	if err != nil {
		return strings.TrimSpace(string(b))
	}
	return strings.TrimSpace(string(state))
}

// UDC returns the bound UDC controller name.
func (g *Gadget) UDC() string { return g.udc }

// Shutdown unbinds the UDC and removes the gadget configfs tree.
func (g *Gadget) Shutdown() error {
	g.fileMu.Lock()
	if g.keyboard != nil {
		g.keyboard.Close()
		g.keyboard = nil
	}
	if g.mouse != nil {
		g.mouse.Close()
		g.mouse = nil
	}
	g.fileMu.Unlock()
	_ = g.unbindUDC()
	return cleanup(g.path)
}

func (g *Gadget) bindUDC() error {
	// dwc2 needs a moment to release the port after an unbind; an immediate rebind
	// can fail with EBUSY.
	var err error
	for i := 0; i < 5; i++ {
		err = writeAttr(filepath.Join(g.path, "UDC"), g.udc)
		if err == nil {
			break
		}
		logging.Debugf("bindUDC(%s) attempt %d: %v - retrying", g.udc, i+1, err)
		time.Sleep(200 * time.Millisecond)
	}
	logging.Debugf("bindUDC(%s): %v", g.udc, err)
	return err
}

func (g *Gadget) unbindUDC() error {
	var lastErr error
	for i := 0; i < 10; i++ {
		// A zero-length write fails with -EFAULT and does not unbind; a whitespace-only
		// value trims to empty, which is what `echo "" > UDC` sends.
		lastErr = writeAttr(filepath.Join(g.path, "UDC"), "\n")
		// Verify the actual state instead of trusting the write result.
		if b, rerr := os.ReadFile(filepath.Join(g.path, "UDC")); rerr == nil && len(bytes.TrimSpace(b)) == 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("UDC still bound after unbind")
}

// cleanup removes the gadget tree: unbind, drop the config symlinks (inner configfs
// groups cannot be rmdir-ed), then rmdir the root and let the kernel remove the rest.
func cleanup(root string) error {
	if !dirExists(root) {
		return nil
	}
	logging.Debugf("cleanup: unbinding UDC in %s", root)
	if err := writeAttr(filepath.Join(root, "UDC"), "\n"); err != nil {
		logging.Debugf("cleanup: UDC unbind: %v (continuing)", err)
	}
	time.Sleep(500 * time.Millisecond) // kernel finishes the unbind first

	cfg := filepath.Join(root, "configs", ConfigName)
	if entries, err := os.ReadDir(cfg); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "hid.usb") ||
				strings.HasPrefix(e.Name(), "mass_storage") {
				_ = os.Remove(filepath.Join(cfg, e.Name()))
			}
		}
	}
	// The per-config label group goes first; the other groups cannot be removed
	// directly and disappear with the root rmdir.
	_ = os.Remove(filepath.Join(cfg, "strings", stringsLangID))
	_ = os.Remove(cfg)

	if funcs, err := os.ReadDir(filepath.Join(root, "functions")); err == nil {
		for _, e := range funcs {
			if e.IsDir() {
				_ = os.Remove(filepath.Join(root, "functions", e.Name()))
			}
		}
	}
	// Top-level string groups hold plain files and are rmdir-able; drop
	// them so the root rmdir's recursive teardown finds an empty tree.
	if langs, err := os.ReadDir(filepath.Join(root, "strings")); err == nil {
		for _, e := range langs {
			if e.IsDir() {
				_ = os.Remove(filepath.Join(root, "strings", e.Name()))
			}
		}
	}
	err := os.Remove(root)
	logging.Debugf("cleanup: root rmdir: %v", err)
	return err
}

func pickUDC(name string) (string, error) {
	if name != "" {
		if !fileExists(filepath.Join(udcClass, name)) {
			return "", fmt.Errorf("UDC %s not present in %s", name, udcClass)
		}
		return name, nil
	}
	entries, err := os.ReadDir(udcClass)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", udcClass, err)
	}
	for _, e := range entries {
		return e.Name(), nil
	}
	return "", fmt.Errorf("no UDC controller registered in %s", udcClass)
}

func loadModules() {
	// usb_configfs is built-in on 6.12.117-ophub; the function modules may
	// be =m. Errors are fine if the capability is built-in or already loaded.
	for _, m := range []string{"libcomposite", "usb_f_hid", "usb_f_mass_storage"} {
		_ = exec.Command("modprobe", m).Run()
	}
}

func writeAttr(path, value string) error {
	return os.WriteFile(path, []byte(value), 0644)
}

func orNone(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}

func fileExists(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && !st.IsDir()
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

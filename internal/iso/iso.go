package iso

/*
	ISO source resolution for the f_mass_storage backing file.

	The NFS share /mnt/shared/iso is the primary pool because the STB eMMC is
	small; the local directory is the fallback. The gadget starts without mass
	storage and picks an ISO up at runtime via POST /api/iso/mount.
*/

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Roots lists the search roots in priority order: the NFS share, then
// ISO_LOCATION_DIR (default /var/lib/kvmostb/iso).
func Roots() []string {
	local := os.Getenv("ISO_LOCATION_DIR")
	if local == "" {
		local = "/var/lib/kvmostb/iso"
	}
	roots := []string{"/mnt/shared/iso", local}
	if roots[0] == roots[1] {
		roots = roots[:1]
	}
	return roots
}

// Entry is one candidate ISO.
type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Source  string `json:"source"` // "nfs" or "local"
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"`
}

// List returns every *.iso in the search roots (newest first).
func List() []Entry {
	var entries []Entry
	for _, root := range Roots() {
		files, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		source := "local"
		if strings.HasPrefix(root, "/mnt/shared") {
			source = "nfs"
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".iso") {
				continue
			}
			fi, err := f.Info()
			if err != nil {
				continue
			}
			entries = append(entries, Entry{
				Name:    f.Name(),
				Path:    filepath.Join(root, f.Name()),
				Source:  source,
				Size:    fi.Size(),
				ModTime: fi.ModTime().Unix(),
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ModTime != entries[j].ModTime {
			return entries[i].ModTime > entries[j].ModTime
		}
		return entries[i].Name > entries[j].Name
	})
	return entries
}

package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alexberardi/jarvis-server/internal/platform/sysinfo"
)

// diskFree is the space available on the filesystem holding a path, 0 when unknown (tests
// replace it).
var diskFree = sysinfo.DiskFree

// ErrDiskFull means there isn't room to stage the upgrade.
var ErrDiskFull = errors.New("not enough free disk space")

// CheckDisk is the free-space preflight before anything is downloaded. The data directory
// holds the archive, the unpacked binary and a snapshot of every database: it needs three
// times the archive (or the archive plus a binary the size of the current one, if larger)
// plus the databases' size. The executable's directory gets the new binary and the jarvisd.prev
// copy: twice the current binary. archive is 0 when the release doesn't say; the current
// binary stands in. A filesystem whose free space can't be read is not checked.
func CheckDisk(p Paths, archive int64) error {
	var exe int64
	if st, err := os.Stat(p.Exe); err == nil {
		exe = st.Size()
	}
	if archive <= 0 {
		archive = exe
	}
	var dbs int64
	files, _ := DatabaseFiles(p.Home)
	for _, f := range files {
		for _, x := range []string{f, f + "-wal"} {
			if st, err := os.Stat(x); err == nil {
				dbs += st.Size()
			}
		}
	}
	home := max(3*archive, archive+exe) + dbs
	if err := needFree(p.Home, home, "the download ×3 and the database snapshots"); err != nil {
		return err
	}
	return needFree(filepath.Dir(p.Exe), 2*exe, "the new binary and the jarvisd.prev copy")
}

func needFree(dir string, need int64, what string) error {
	free := diskFree(dir)
	if free == 0 || need <= 0 || free >= uint64(need) {
		return nil
	}
	return fmt.Errorf("%w in %s: the upgrade needs about %s there (%s), %s is free; free some space and try again",
		ErrDiskFull, dir, mib(uint64(need)), what, mib(free))
}

func mib(n uint64) string { return fmt.Sprintf("%d MB", (n+(1<<20)-1)>>20) }

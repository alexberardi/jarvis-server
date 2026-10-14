package update

import (
	"fmt"
	"os"
	"time"
)

// Windows lets another process hold a file open without FILE_SHARE_DELETE, and while it does
// the file can't be renamed (ERROR_SHARING_VIOLATION). Antivirus (Defender) does exactly that
// for a few seconds to an executable that was just written or just started, which is every
// binary a swap or a rollback puts in place. An update right after a rollback (or a swap whose
// freshly extracted .jarvisd.new is still being scanned) then failed once with "move the
// running binary aside: ... being used by another process" (CI, PR #127). The lock is
// transient, so the swap's renames retry it with bounded backoff, as the Go toolchain does for
// the same reason (cmd/go/internal/robustio).

// Seams for tests: the rename, the wait and what counts as a transient lock.
var (
	renameFile      = os.Rename
	sleepFor        = time.Sleep
	isTransientLock = transientLock // per OS: never on unix
)

// renameRetryBudget bounds the total wait of one renameRetry. A scan of a ~100 MB executable
// is seconds; the helper's caller waits minutes (helperWait).
const renameRetryBudget = 20 * time.Second

// renameRetry is os.Rename, retried while the error is a transient lock (Windows sharing or
// lock violation, or access denied while a handle is closing) with backoff from 10 ms doubling
// to 1 s, until renameRetryBudget of waiting is spent. Any other error returns at once. The
// waiting is summed from the schedule, not read from a clock, so tests are deterministic.
func renameRetry(oldpath, newpath string) error {
	delay := 10 * time.Millisecond
	var waited time.Duration
	for {
		err := renameFile(oldpath, newpath)
		if err == nil || !isTransientLock(err) {
			return err
		}
		if waited >= renameRetryBudget {
			return fmt.Errorf("%w (still locked after %s)", err, waited)
		}
		sleepFor(delay)
		waited += delay
		delay = min(delay*2, time.Second)
	}
}

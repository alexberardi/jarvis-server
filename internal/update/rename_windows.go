package update

import (
	"errors"

	"golang.org/x/sys/windows"
)

// transientLock reports an error another process's open handle causes and that clears once it
// closes: a sharing or lock violation, or access denied (a file whose handle is still closing,
// or is pending delete). The swap only gets this far after CanWrite, so access denied is not a
// missing privilege here, and the retry is bounded either way.
func transientLock(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

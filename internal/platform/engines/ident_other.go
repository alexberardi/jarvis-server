//go:build !darwin && !linux

package engines

import "errors"

// procIdentity is unsupported here: Windows ties engines to jarvisd with a job object, and
// other systems write no pid files.
func procIdentity(int) (exe, start string, err error) { return "", "", errors.ErrUnsupported }

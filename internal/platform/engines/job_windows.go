package engines

import (
	"fmt"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Engines go into one job object per jarvisd process, created with KILL_ON_JOB_CLOSE.
// jarvisd holds the only handle, so when it exits for any reason (clean stop, crash, the
// SCM killing it) Windows closes the handle and kills every engine still in the job: an
// orphaned llama-server can't keep the GPU or its port (I0 finding 3).
var (
	jobOnce sync.Once
	job     windows.Handle
	jobErr  error
)

func killOnCloseJob() (windows.Handle, error) {
	jobOnce.Do(func() { job, jobErr = newKillOnCloseJob() })
	return job, jobErr
}

func newKillOnCloseJob() (windows.Handle, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return 0, fmt.Errorf("set job limits: %w", err)
	}
	return h, nil
}

// assign puts the process in the job. Children it starts later join the job too.
func assign(job windows.Handle, pid int) error {
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(p)
	return windows.AssignProcessToJobObject(job, p)
}

// contain ties the engine's lifetime to jarvisd's.
func contain(p *os.Process) error {
	j, err := killOnCloseJob()
	if err != nil {
		return err
	}
	return assign(j, p.Pid)
}

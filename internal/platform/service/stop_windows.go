package service

// StopBlocker says why the admin Stop button (AD8b) can't keep jarvisd stopped ("" when it can).
// Under the SCM it always can: the recovery actions fire on a failure, and a service that
// reports SERVICE_STOPPED with exit code 0 (scmExit for a stop) has not failed. Every jarvisd
// version registered the service that way, so there is no stale definition to refuse.
func StopBlocker(Kind, bool) (reason, command string) { return "", "" }

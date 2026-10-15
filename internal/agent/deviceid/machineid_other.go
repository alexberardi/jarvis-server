//go:build !linux && !darwin && !windows

package deviceid

func machineID() (string, error) { return "", errNoMachineID }

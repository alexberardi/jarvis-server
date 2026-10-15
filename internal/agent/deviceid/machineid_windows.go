package deviceid

import "golang.org/x/sys/windows/registry"

// machineID is Windows' MachineGuid, set at install time. The 64-bit view: a 32-bit process
// would otherwise read the WOW64 copy, which lacks it.
func machineID() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", errNoMachineID
	}
	return v, nil
}

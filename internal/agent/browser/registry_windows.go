package browser

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// NativeRegistry is HKEY_CURRENT_USER. Browsers find a native-messaging host through
// HKCU\Software\<vendor>\NativeMessagingHosts\<name> (default value: the manifest's path).
func NativeRegistry() Registry { return winRegistry{} }

type winRegistry struct{}

func (winRegistry) SetDefault(key, value string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("", value)
}

func (winRegistry) GetDefault(key string) (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue("")
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return v, err
}

func (winRegistry) DeleteKey(key string) error {
	err := registry.DeleteKey(registry.CURRENT_USER, key)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

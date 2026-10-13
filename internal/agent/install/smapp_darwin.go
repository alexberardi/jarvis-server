package install

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// SMAppService (ServiceManagement, macOS 13+), over purego like the island: no cgo.
var (
	smOnce  sync.Once
	smClass objc.Class
	smErr   error
)

func loadSM() error {
	smOnce.Do(func() {
		for _, lib := range []string{"/usr/lib/libobjc.A.dylib",
			"/System/Library/Frameworks/Foundation.framework/Foundation",
			"/System/Library/Frameworks/ServiceManagement.framework/ServiceManagement"} {
			if _, err := purego.Dlopen(lib, purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
				smErr = err
				return
			}
		}
		if smClass = objc.GetClass("SMAppService"); smClass == 0 {
			smErr = errors.New("SMAppService needs macOS 13 or later")
		}
	})
	return smErr
}

// SMAppServiceAvailable reports whether SMAppService exists (macOS 13+).
func SMAppServiceAvailable() bool { return loadSM() == nil }

func nativeLoginItems() LoginItems { return smAppService{} }

type smAppService struct{}

// SMAppServiceStatus values.
const (
	smNotRegistered    = 0
	smEnabled          = 1
	smRequiresApproval = 2
	smNotFound         = 3
)

func (smAppService) service(plist string) (objc.ID, error) {
	if err := loadSM(); err != nil {
		return 0, err
	}
	nsName := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), plist)
	svc := objc.ID(smClass).Send(objc.RegisterName("agentServiceWithPlistName:"), nsName)
	if svc == 0 {
		return 0, fmt.Errorf("SMAppService: no service for %s", plist)
	}
	return svc, nil
}

func nsErrorText(e objc.ID) string {
	if e == 0 {
		return "unknown error"
	}
	d := e.Send(objc.RegisterName("localizedDescription"))
	if d == 0 {
		return "unknown error"
	}
	return objc.Send[string](d, objc.RegisterName("UTF8String"))
}

// Register registers the app's LaunchAgent. Already registered is success; "requires approval"
// means the user switched it off in System Settings → General → Login Items, which only they
// can undo.
func (s smAppService) Register(plist string) error {
	svc, err := s.service(plist)
	if err != nil {
		return err
	}
	switch objc.Send[int](svc, objc.RegisterName("status")) {
	case smEnabled:
		return nil
	case smRequiresApproval:
		return errors.New("macOS needs your approval: turn on Jarvis Automation Agent in System Settings → General → Login Items")
	}
	var nsErr objc.ID
	if ok := objc.Send[bool](svc, objc.RegisterName("registerAndReturnError:"), &nsErr); !ok {
		return fmt.Errorf("SMAppService register: %s", nsErrorText(nsErr))
	}
	return nil
}

// Unregister removes the login item; one that isn't registered is fine.
func (s smAppService) Unregister(plist string) error {
	svc, err := s.service(plist)
	if err != nil {
		return err
	}
	if st := objc.Send[int](svc, objc.RegisterName("status")); st == smNotRegistered || st == smNotFound {
		return nil
	}
	var nsErr objc.ID
	if ok := objc.Send[bool](svc, objc.RegisterName("unregisterAndReturnError:"), &nsErr); !ok {
		return fmt.Errorf("SMAppService unregister: %s", nsErrorText(nsErr))
	}
	return nil
}

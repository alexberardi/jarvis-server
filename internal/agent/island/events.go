package island

// AppEvents are what the OS asks of the running app while the island's event loop runs. On
// macOS, once `run` has called NSApplication's finishLaunching, LaunchServices treats the agent
// as "Jarvis Automation Agent.app" running: opening the app again (Finder, the Dock, Spotlight)
// or the jarvis-agent://start link starts no process, but sends the running agent an Apple event
// (reopen, or get-URL). These are where they go. Elsewhere a second launch is a new process and
// reaches the agent over its control socket, so they are never called.
type AppEvents struct {
	// Reopen: the app was opened again (kAEReopenApplication).
	Reopen func()
	// OpenURL: a URL of the app's scheme was opened (kAEGetURL); the receiver checks it.
	OpenURL func(url string)
	// StopHotkey: the user pressed ⌃⌥⎋ (StopHotkeyLabel), anywhere: stop what Jarvis is doing
	// with the computer. macOS only for now.
	StopHotkey func()
}

// StopHotkeyLabel is the stop hotkey as the tray's tooltip names it.
const StopHotkeyLabel = "⌃⌥⎋"

// The stop hotkey as Carbon's RegisterEventHotKey takes it.
const (
	stopKeyCode   = 53              // kVK_Escape
	stopModifiers = 0x1000 | 0x0800 // controlKey | optionKey
)

// hotkeySig is the signature of our hotkeys' EventHotKeyID.
var hotkeySig = fourCC("jarv")

// hotkey hands a press of the stop hotkey to StopHotkey, on a goroutine (the caller is the main
// thread).
func (ev AppEvents) hotkey() {
	if ev.StopHotkey != nil {
		go ev.StopHotkey()
	}
}

// fourCC is a Mac four-character code ("aevt") as its 32-bit value.
func fourCC(s string) uint32 {
	return uint32(s[0])<<24 | uint32(s[1])<<16 | uint32(s[2])<<8 | uint32(s[3])
}

// The Apple events the agent handles.
var (
	aeCoreClass     = fourCC("aevt") // kCoreEventClass
	aeReopen        = fourCC("rapp") // kAEReopenApplication
	aeInternetClass = fourCC("GURL") // kInternetEventClass
	aeGetURL        = fourCC("GURL") // kAEGetURL
	keyDirectObject = fourCC("----")
)

// dispatch hands an Apple event to its handler, on a goroutine: the caller is AppKit's main
// thread, which the handlers must never block (the island itself needs it). It reports whether
// the event was one of ours.
func (ev AppEvents) dispatch(class, id uint32, url string) bool {
	switch {
	case class == aeCoreClass && id == aeReopen:
		if ev.Reopen != nil {
			go ev.Reopen()
		}
		return true
	case class == aeInternetClass && id == aeGetURL:
		if ev.OpenURL != nil {
			go ev.OpenURL(url)
		}
		return true
	}
	return false
}

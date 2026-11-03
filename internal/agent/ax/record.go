package ax

import "strings"

// Recorded is an element as a desktop recording keeps it (plan P3 piece 36): what it is, where
// it sits in its app and whose it is.
type Recorded struct {
	Role   string // AXSecureTextField for a secure text field
	Name   string
	Secure bool
	// Path is the roles from the window down to the element, each with its AXIdentifier when
	// the app sets one: "AXWindow/AXSplitGroup/AXScrollArea/AXTextArea#body".
	Path string
	PID  int
	App  string // the app's bundle id; "" when unknown
	// NoContext: the app isn't on the accessibility bus (Wayland: foot, a Qt app without
	// accessibility), so its event has no context.
	NoContext bool
}

// RecordedAt returns the element under global point (x, y) for a recorded click; nil when
// there's none.
func RecordedAt(x, y float64) (*Recorded, error) { return recordedAt(x, y) }

// FocusedElement returns the element with the keyboard focus, which recorded typing goes to;
// nil when there's none.
func FocusedElement() (*Recorded, error) { return focusedElement() }

// ForgetApps drops the bundle ids remembered per pid (a pid can be reused once its app quits):
// each recording starts with none.
func ForgetApps() { forgetApps() }

const maxPathDepth = 32

// recordedOf reads n's identity and path. parent returns a node's AXParent (owned by the
// caller) or nil.
func recordedOf(n axNode, parent func(axNode) axNode) Recorded {
	role, secure := identity(n)
	name, _ := describe(n, role, secure)
	return Recorded{Role: role, Name: name, Secure: secure, Path: elementPath(n, role, parent)}
}

// elementPath walks up from n (whose role is role) to its application, which is left out.
func elementPath(n axNode, role string, parent func(axNode) axNode) string {
	segs := []string{segment(n, role)}
	cur, owned := n, false
	for range maxPathDepth {
		p := parent(cur)
		if owned {
			cur.release()
		}
		if p == nil {
			owned = false
			break
		}
		cur, owned = p, true
		r := p.attr("AXRole")
		if r == "AXApplication" || r == "AXSystemWide" {
			break
		}
		segs = append(segs, segment(p, r))
	}
	if owned {
		cur.release()
	}
	for i, j := 0, len(segs)-1; i < j; i, j = i+1, j-1 {
		segs[i], segs[j] = segs[j], segs[i]
	}
	return strings.Join(segs, "/")
}

func segment(n axNode, role string) string {
	if role == "" {
		role = "AXUnknown"
	}
	if id := n.attr("AXIdentifier"); id != "" {
		return role + "#" + strings.ReplaceAll(id, "/", "_")
	}
	return role
}

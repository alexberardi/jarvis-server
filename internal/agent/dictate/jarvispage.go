package dictate

import (
	"context"
	"errors"
	"fmt"
)

// The Jarvis page in front (DESIGN.md §14; web app plan §3.6): someone chatting with Jarvis in
// the web page on this computer asks it to type something. With no app named, the text would
// go into the window in front: the chat page itself. So the agent types into the window that
// was in front before it instead, where the backend keeps a focus history, confirming focus as
// for a named app; and where it doesn't (GNOME and KDE on Wayland), it types nothing and asks
// which app.

// CodeJarvisPage is the error code of typing refused because the Jarvis page is in front.
const CodeJarvisPage = "jarvis_page_in_front"

// pageError: the Jarvis page is in front and there is no window to use instead.
type pageError struct {
	// pickable: the user can name an app (window control works here); otherwise they must
	// click into it.
	pickable bool
	why      string
}

func (e *pageError) Error() string     { return e.why }
func (e *pageError) ErrorCode() string { return CodeJarvisPage }

// awayFromJarvisPage is the target for typing with no app named: nil (the window in front, as
// before) unless the Jarvis page is in front, then the window used before it.
func (t *Tools) awayFromJarvisPage(ctx context.Context) (*target, error) {
	if t.JarvisPage == nil {
		return nil, nil
	}
	if t.Win == nil {
		return nil, t.pageBlind(ctx)
	}
	if ok, _ := t.Win.Available(ctx); !ok {
		return nil, t.pageBlind(ctx)
	}
	active, err := t.Win.Active(ctx)
	if err != nil {
		return nil, t.pageBlind(ctx)
	}
	if active == "" {
		return nil, nil // no window has focus: front() says so
	}
	var ws []Window
	history := false
	if fo, ok := t.Win.(FocusOrdered); ok {
		ws, history, err = fo.ByFocus(ctx)
	} else {
		ws, err = t.Win.List(ctx)
	}
	if err != nil {
		return nil, t.pageBlind(ctx)
	}
	front := -1
	for i, w := range ws {
		if t.Win.Same(w.ID, active) {
			front = i
			break
		}
	}
	if front < 0 || !t.JarvisPage(ctx, &ws[front]) {
		return nil, nil
	}
	if !history {
		return nil, &pageError{pickable: true, why: "the Jarvis page is in front, and this desktop can't tell which window was used before it"}
	}
	prev := -1
	for i, w := range ws {
		if i == front || w.ID == ws[front].ID {
			continue
		}
		if prev < 0 || w.Recency < ws[prev].Recency {
			prev = i
		}
	}
	if prev < 0 {
		return nil, &pageError{pickable: true, why: "the Jarvis page is in front, and no other window is open"}
	}
	tg := t.targetOf(ws[prev], "", false)
	tg.insteadOfPage = true
	t.log().Info("dictation: the Jarvis page is in front; typing into the window used before it", "app", tg.app)
	return tg, nil
}

// pageBlind is for a desktop whose windows can't be listed (GNOME, KDE): only the browser
// extension can say the Jarvis page is in front, and then nothing is typed.
func (t *Tools) pageBlind(ctx context.Context) error {
	if !t.JarvisPage(ctx, nil) {
		return nil
	}
	return &pageError{why: "the Jarvis page is in front, and on this desktop the agent can't switch to another app"}
}

// pageFailure is FailureText for a pageError.
func pageFailure(what, tell string, err error) (string, bool) {
	var pe *pageError
	if !errors.As(err, &pe) {
		return "", false
	}
	if pe.pickable {
		return fmt.Sprintf("%s: %s, and typing there would only go into the chat. Tell the user %s and ask which app "+
			"to type into, then call again with that app.", what, pe.why, tell), true
	}
	return fmt.Sprintf("%s: %s, and typing there would only go into the chat. Tell the user %s and ask which app "+
		"it's for: they need to click into that app, then ask again.", what, pe.why, tell), true
}

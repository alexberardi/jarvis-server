package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/alexberardi/jarvis-server/internal/agent/portal"
)

// dictationCmd manages the one-time typing permission GNOME and KDE on Wayland need
// (DESIGN.md §14): `allow` shows the desktop's dialog once and keeps the restore token,
// `revoke` forgets it, `status` says where things stand. Nothing else ever asks for it.
func dictationCmd(ctx context.Context, args []string) error {
	rd := &portal.RemoteDesktop{}
	if runtime.GOOS == "linux" {
		// A grant made under the old app id can't be restored: forget it, so status says so.
		_, _ = portal.MigrateLegacy(os.Getenv, nil)
	}
	return dictationRun(ctx, args, rd, os.Stdout, runtime.GOOS, desktopFile{})
}

// desktopFile is the net.jarvisautomation.agent.desktop file the portal needs to know the agent by its app
// id (portal.AppID), whoever started it.
type desktopFile struct{}

func (desktopFile) path() (string, error) { return portal.DesktopFilePath(os.Getenv) }

func (d desktopFile) Write() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	p, err := d.path()
	if err != nil {
		return err
	}
	return portal.WriteDesktopFile(p, exe)
}

func (d desktopFile) Remove() error {
	p, err := d.path()
	if err != nil {
		return err
	}
	return portal.RemoveDesktopFile(p)
}

// appEntry is desktopFile (a fake in tests).
type appEntry interface {
	Write() error
	Remove() error
}

// dictationPortal is the part of portal.RemoteDesktop the command uses (a fake in tests).
type dictationPortal interface {
	Probe(ctx context.Context) portal.State
	Allow(ctx context.Context) error
	Revoke() error
}

func dictationRun(ctx context.Context, args []string, rd dictationPortal, w io.Writer, goos string, app appEntry) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: jarvis-agent dictation allow | revoke | status")
	}
	if goos != "linux" {
		return fmt.Errorf("only GNOME and KDE on Linux need this; on %s dictation uses the OS's own permission (see jarvis-agent status)", goos)
	}
	switch args[0] {
	case "status":
		st := rd.Probe(ctx)
		switch {
		case st.Allowed:
			fmt.Fprintln(w, "typing through the desktop portal: allowed")
		case st.Portal:
			fmt.Fprintln(w, "typing through the desktop portal: not allowed yet; run `"+portal.AllowCommand+"`")
		default:
			fmt.Fprintln(w, "typing through the desktop portal: unavailable:", st.Why)
		}
		return nil
	case "allow":
		// Before the portal connection: it registers as portal.AppID only if this file exists.
		if err := app.Write(); err != nil {
			fmt.Fprintln(w, "note: couldn't write the agent's .desktop file, so the permission may be tied to this terminal:", err)
		}
		fmt.Fprint(w, "Your desktop will now ask whether Jarvis Automation Agent may control the keyboard.\n"+
			"Allow it (the button may say Share or Allow), and if the dialog offers to remember the\n"+
			"choice, leave that on. It asks only this once.\n")
		if err := rd.Allow(ctx); err != nil {
			return err
		}
		fmt.Fprintln(w, "Allowed. Dictation types into the window in front; no restart needed.")
		return nil
	case "revoke":
		if err := rd.Revoke(); err != nil {
			return err
		}
		_ = app.Remove()
		fmt.Fprintln(w, "Forgotten. Your desktop's privacy settings may still list the old grant; you can remove it there too.")
		return nil
	}
	return fmt.Errorf("usage: jarvis-agent dictation allow | revoke | status")
}

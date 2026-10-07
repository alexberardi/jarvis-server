package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/doctor"
	"github.com/alexberardi/jarvis-server/internal/platform/service"
)

// uninstallOptions are `jarvisd service uninstall`'s flags, plus test seams.
type uninstallOptions struct {
	Purge        bool // also delete the data directory, env file and service account
	Yes          bool // don't ask before purging
	KeepFirewall bool // leave the firewall rules `doctor --fix` added

	firewall doctor.Firewall // default: the host's
	run      doctor.Runner   // default: doctor.FixRunner
	elevated func() bool     // default: doctor.Elevated
}

// uninstall removes the service definition, then the firewall rules `doctor --fix` added
// (00-installers §4.4), and with Purge the data directory, env file and account, after a
// typed confirmation of the path (or --yes). Binaries are the install scripts' to remove:
// a running jarvisd.exe can't delete itself.
func uninstall(ctx context.Context, m service.Manager, o uninstallOptions, in io.Reader, interactive bool, out io.Writer) error {
	home := m.InstalledHome()
	var plan service.PurgePlan
	if o.Purge {
		if home == "" {
			return errors.New("--purge: no installed jarvisd service names a data directory; nothing was changed")
		}
		plan = m.PurgePlan(home)
		if err := service.CheckPurgeHome(plan.Home); err != nil {
			return err
		}
		if err := confirmPurge(plan, o.Yes, in, interactive, out); err != nil {
			return err
		}
	}
	err := m.Uninstall(ctx)
	if err != nil && !errors.Is(err, service.ErrNotInstalled) {
		return err
	}
	// Rules from an earlier install go even when the service is already gone.
	if !o.KeepFirewall {
		removeFirewall(ctx, o, out)
	}
	if err != nil {
		return err
	}
	if o.Purge {
		if err := m.Purge(ctx, plan); err != nil {
			return fmt.Errorf("purge: %w", err)
		}
		fmt.Fprintf(out, "deleted %s\n", strings.Join(purgeList(plan), ", "))
	}
	return nil
}

func removeFirewall(ctx context.Context, o uninstallOptions, out io.Writer) {
	fw := o.firewall
	if fw == nil {
		fw = doctor.HostFirewall("")
	}
	if fw == nil {
		return
	}
	elevated := o.elevated
	if elevated == nil {
		elevated = doctor.Elevated
	}
	if !elevated() {
		if cmds := fw.RemoveCmds(ctx); len(cmds) > 0 {
			fmt.Fprintf(out, "the firewall rules jarvisd added are still there (removing them needs root); to remove them:\n  %s\n",
				strings.ReplaceAll(doctor.RenderFix(runtime.GOOS, cmds, ""), "\n", "\n  "))
		}
		return
	}
	if err := doctor.RemoveFirewallRules(ctx, fw, o.run, out); err != nil {
		fmt.Fprintf(out, "warning: removing jarvisd's firewall rules: %v\n", err)
	}
}

// purgeList names everything a purge deletes, for the prompt and the report.
func purgeList(p service.PurgePlan) []string {
	items := append([]string{p.Home}, p.Extra...)
	if p.Account != "" {
		items = append(items, "the "+p.Account+" account")
	}
	return items
}

// confirmPurge shows what --purge deletes and asks for the data directory's path to be typed
// back. Without a terminal it needs --yes.
func confirmPurge(p service.PurgePlan, yes bool, in io.Reader, interactive bool, out io.Writer) error {
	fmt.Fprintf(out, "--purge deletes, for good:\n  %s (%s: the database with every account, node and memory, plus models and logs)\n",
		p.Home, humanBytes(service.DirSize(p.Home)))
	for _, e := range p.Extra {
		fmt.Fprintf(out, "  %s\n", e)
	}
	if p.Account != "" {
		fmt.Fprintf(out, "  the %s account\n", p.Account)
	}
	if yes {
		return nil
	}
	if !interactive {
		return errors.New("--purge needs confirmation: run it at a terminal, or add --yes")
	}
	fmt.Fprintf(out, "Type the data directory's path to confirm: ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	if filepath.Clean(strings.TrimSpace(line)) != filepath.Clean(p.Home) {
		return errors.New("not confirmed; nothing was changed")
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// stdinIsTerminal reports whether someone can answer a prompt.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

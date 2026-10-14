package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/settings"
)

// applySettings sets run's flags from the config file (path, or the default one), leaving the
// flags the command line gave alone. A broken file stops the agent: running with a choice such
// as confirm_dictation silently dropped would be worse.
func applySettings(fs *flag.FlagSet, path string, log *slog.Logger) error {
	if path == "" {
		var err error
		if path, err = settings.DefaultPath(); err != nil {
			return err
		}
	}
	s, unknown, err := settings.Load(path)
	if err != nil {
		return fmt.Errorf("config file: %w (fix or delete it; `jarvis-agent config` shows the keys)", err)
	}
	if len(unknown) > 0 {
		log.Warn("config file: ignoring unknown keys", "path", path, "keys", unknown)
	}
	return s.Apply(fs)
}

// configCmd shows and edits the settings file: show (the default), path, set KEY VALUE,
// unset KEY.
func configCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(w)
	file := fs.String("file", "", "settings file (default: <user config dir>/jarvis-agent/agent.json)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := *file
	if path == "" {
		var err error
		if path, err = settings.DefaultPath(); err != nil {
			return err
		}
	}
	args = fs.Args()
	verb := "show"
	if len(args) > 0 {
		verb, args = args[0], args[1:]
	}
	usage := errors.New("usage: jarvis-agent config [show | path | set KEY VALUE | unset KEY]; keys: " + settings.KeyNames())
	switch verb {
	case "path":
		fmt.Fprintln(w, path)
		return nil
	case "show":
		s, unknown, err := settings.Load(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "# %s (unset keys use the run flag's default; flags given to run win)\n", path)
		for _, k := range settings.Keys {
			v, ok := s.Get(k)
			if !ok {
				v = "(unset)"
			}
			fmt.Fprintf(w, "%-18s %-12s %s\n", k.Name, v, k.Help)
		}
		if len(unknown) > 0 {
			fmt.Fprintf(w, "unknown keys, ignored: %s\n", strings.Join(unknown, ", "))
		}
		return nil
	case "set", "unset":
		if (verb == "set" && len(args) != 2) || (verb == "unset" && len(args) != 1) {
			return usage
		}
		k, ok := settings.Lookup(args[0])
		if !ok {
			return fmt.Errorf("unknown key %q; keys: %s", args[0], settings.KeyNames())
		}
		s, _, err := settings.Load(path)
		if err != nil {
			return err
		}
		if verb == "set" {
			if err := s.Set(k, args[1]); err != nil {
				return err
			}
		} else {
			s.Unset(k)
		}
		if err := settings.Save(path, s); err != nil {
			return err
		}
		fmt.Fprintln(w, "Saved. A running agent picks it up when it restarts.")
		return nil
	}
	return usage
}

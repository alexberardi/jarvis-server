package main

import (
	"flag"
	"fmt"
	"strings"
)

// noStrayArgs refuses positional arguments left after a command's flags. Without it they were
// dropped silently: `jarvis-agent run -- --voice` ran without voice, because the flag package
// stops at "--" and treats the rest as arguments nobody read.
func noStrayArgs(fs *flag.FlagSet) error {
	if fs.NArg() == 0 {
		return nil
	}
	hint := ""
	for _, a := range fs.Args() {
		if strings.HasPrefix(a, "-") {
			hint = fmt.Sprintf(" (flags go before any \"--\": jarvis-agent %s %s)", fs.Name(), a)
			break
		}
	}
	return fmt.Errorf("%s takes no arguments, but got %q%s", fs.Name(), fs.Args(), hint)
}

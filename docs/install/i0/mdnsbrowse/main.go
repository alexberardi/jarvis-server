// Command mdnsbrowse is an I0 spike helper, not product code: it browses _jarvis-config._tcp
// with the same library jarvisd advertises with and prints every address each instance
// announces (avahi-browse and dns-sd show only one).
//
//	mdnsbrowse [-t 5s]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/grandcat/zeroconf"
)

func main() {
	timeout := flag.Duration("t", 5*time.Second, "how long to browse")
	flag.Parse()
	r, err := zeroconf.NewResolver(nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolver:", err)
		os.Exit(1)
	}
	ch := make(chan *zeroconf.ServiceEntry)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	done := make(chan struct{})
	n := 0
	go func() {
		defer close(done)
		for e := range ch {
			n++
			fmt.Printf("%q host=%s port=%d v4=%v v6=%v txt=%v\n", e.Instance, e.HostName, e.Port, e.AddrIPv4, e.AddrIPv6, e.Text)
		}
	}()
	if err := r.Browse(ctx, "_jarvis-config._tcp", "local.", ch); err != nil {
		fmt.Fprintln(os.Stderr, "browse:", err)
		os.Exit(1)
	}
	<-ctx.Done()
	<-done
	fmt.Printf("%d instance(s)\n", n)
}

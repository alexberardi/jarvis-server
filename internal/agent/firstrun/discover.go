package firstrun

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Where a server was found.
const (
	WhereHere    = "this computer"
	WhereNetwork = "network"
	WhereTyped   = "typed"
)

// Found is a Jarvis Automation server the page can join.
type Found struct {
	URL   string `json:"url"`
	Name  string `json:"name"`
	Where string `json:"where"`
}

// DefaultPort is the config service's port, assumed when a typed address has none.
const DefaultPort = "7700"

// NormalizeAddress turns what someone typed ("192.168.1.10", "jarvis.local:7700",
// "http://host:7700/") into a config service URL, or says what's wrong in plain words.
func NormalizeAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("Type the server's address, like 192.168.1.10.")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	bad := errors.New("That doesn't look like an address. Try something like 192.168.1.10 or jarvis.local.")
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return "", bad
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			host = u.Host
		} else {
			host = net.JoinHostPort(u.Hostname(), DefaultPort)
		}
	}
	return u.Scheme + "://" + host, nil
}

// isConfigService asks base/info whether it is Jarvis Automation's config service.
func isConfigService(ctx context.Context, hc *http.Client, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/info", nil)
	if err != nil {
		return false
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var info struct {
		Service string `json:"service"`
	}
	return resp.StatusCode == http.StatusOK && decodeSmall(resp, &info) == nil && info.Service == "jarvis-config-service"
}

// finder is the server search: this computer first, then the network.
type finder struct {
	d Deps
	// isLocal: the URL is this computer (isLocalURL; tests swap it).
	isLocal func(string) bool

	mu        sync.Mutex
	found     []Found
	searching bool
	gen       int // the search running now
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// search starts a new search (ending one in progress), forgetting what was found before.
func (f *finder) search(parent context.Context) {
	f.mu.Lock()
	if f.cancel != nil {
		f.cancel()
	}
	ctx, cancel := context.WithCancel(parent)
	f.gen++
	gen := f.gen
	f.cancel, f.found, f.searching = cancel, nil, true
	f.wg.Add(1)
	f.mu.Unlock()
	go func() {
		defer f.wg.Done()
		defer cancel()
		f.run(ctx, gen)
		f.mu.Lock()
		if f.gen == gen {
			f.searching = false
		}
		f.mu.Unlock()
	}()
}

func (f *finder) run(ctx context.Context, gen int) {
	here := isConfigService(ctx, f.d.HTTP, f.d.Loopback)
	if here {
		f.add(gen, Found{URL: f.d.Loopback, Name: f.d.ComputerName, Where: WhereHere})
	}
	if f.d.Browse == nil {
		return
	}
	bctx, cancel := context.WithTimeout(ctx, f.d.BrowseFor)
	defer cancel()
	var probes sync.WaitGroup
	f.d.Browse(bctx, func(u, name string) {
		if here && f.isLocal(u) {
			return // this computer, already listed as such
		}
		probes.Add(1)
		go func() {
			defer probes.Done()
			if isConfigService(ctx, f.d.HTTP, u) {
				f.add(gen, Found{URL: u, Name: name, Where: WhereNetwork})
			}
		}()
	})
	probes.Wait()
}

// add lists a server once (by URL, and by name for the network: one entry per server even
// when it answers on several addresses).
func (f *finder) add(gen int, s Found) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if gen != f.gen {
		return // an older search's late answer
	}
	for _, x := range f.found {
		if x.URL == s.URL || (s.Where == WhereNetwork && x.Where == WhereNetwork && x.Name == s.Name) {
			return
		}
	}
	f.found = append(f.found, s)
}

func (f *finder) list() ([]Found, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Found(nil), f.found...), f.searching
}

func (f *finder) close() {
	f.mu.Lock()
	if f.cancel != nil {
		f.cancel()
	}
	f.mu.Unlock()
	f.wg.Wait()
}

// isLocalURL: the URL's host is one of this computer's own addresses.
func isLocalURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// chosen is the server the user picked, with the URLs joining needs.
type chosen struct {
	Found
	cc, auth string
}

// choose checks a server can be joined: it is a config service, and lists command-center and
// auth.
func choose(ctx context.Context, hc *http.Client, s Found) (chosen, error) {
	if !isConfigService(ctx, hc, s.URL) {
		return chosen{}, errors.New("No Jarvis Automation server answered at " + s.URL + ". Check the address, and that the server is on.")
	}
	svcs, err := node.Discover(ctx, hc, s.URL)
	if err != nil {
		return chosen{}, errors.New("That server answered, but couldn't say how to join it. Try again in a minute.")
	}
	cc, err1 := svcs.Need(node.SvcCommandCenter)
	auth, err2 := svcs.Need(node.SvcAuth)
	if err1 != nil || err2 != nil {
		return chosen{}, errors.New("That server isn't ready to add computers yet. Finish its setup first.")
	}
	return chosen{Found: s, cc: cc, auth: auth}, nil
}

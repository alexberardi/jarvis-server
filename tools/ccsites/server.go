package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

// Server serves the four fixture sites and the control API. Its state (orders, downloads, forms,
// sessions and carts) lives in memory and, with a state dir, in DIR/state.json after every change.
//
//	/store/   Test Store: search, product, compare, cart, checkout, order confirmation (store.go)
//	/bank/    Test Bank: monthly statements with a CSV download (bank.go)
//	/card/    Test Card Services: the card's statements, same pages (bank.go)
//	/form/    a 10-field application form with validation (form.go)
//	/control/ state, reset and login for the suite (control.go)
//
// store, bank and card answer 401 with a sign-in page without their session cookie.
// ?layout=moved on store and bank pages moves the primary buttons (other position, wrapper and
// id, same accessible name) and is carried on through links and forms.
type Server struct {
	dir   string // "" = memory only
	now   func() time.Time
	pages map[string]*template.Template

	mu    sync.Mutex
	st    State
	carts map[string][]CartItem // session token → cart
}

// State is what GET /control/state reports; every list is [] when empty, never null.
type State struct {
	Orders    []Order    `json:"orders"`
	Downloads []Download `json:"downloads"`
	Forms     []FormSub  `json:"forms"`
	Sessions  []Session  `json:"sessions"`
}

type Order struct {
	Number string     `json:"number"`
	Time   time.Time  `json:"time"`
	Items  []CartItem `json:"items"`
	Total  float64    `json:"total"`
	ShipTo Address    `json:"ship_to"`
	Card   string     `json:"card"`
}

type CartItem struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
	Qty   int     `json:"qty"`
}

type Address struct {
	Name   string `json:"name"`
	Street string `json:"street"`
	City   string `json:"city"`
	Zip    string `json:"zip"`
}

type Download struct {
	Site     string    `json:"site"`
	Month    string    `json:"month"`
	Filename string    `json:"filename"`
	Rows     int       `json:"rows"`
	Time     time.Time `json:"time"`
}

type FormSub struct {
	ID     string            `json:"id"`
	Time   time.Time         `json:"time"`
	Fields map[string]string `json:"fields"`
}

type Session struct {
	Site    string    `json:"site"`
	Token   string    `json:"token"`
	Created time.Time `json:"created"`
}

// saved is the state file: the public state plus the carts, so a restart keeps a half-done errand.
type saved struct {
	State
	Carts map[string][]CartItem `json:"carts"`
}

// sites with a login wall, and their names on the sign-in page.
var walled = map[string]string{"store": "Test Store", "bank": "Test Bank", "card": "Test Card Services"}

func cookieName(site string) string { return "ccsites_" + site }

// NewServer loads DIR/state.json when dir is set and it exists.
func NewServer(dir string) (*Server, error) {
	s := &Server{dir: dir, now: time.Now, pages: map[string]*template.Template{}, carts: map[string][]CartItem{}}
	s.st = emptyState()
	names, err := templateFS.ReadDir("templates")
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if n.Name() == "base.html" {
			continue
		}
		t, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/base.html", "templates/"+n.Name())
		if err != nil {
			return nil, err
		}
		s.pages[strings.TrimSuffix(n.Name(), ".html")] = t
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		b, err := os.ReadFile(filepath.Join(dir, "state.json"))
		if err == nil {
			var sv saved
			if err := json.Unmarshal(b, &sv); err != nil {
				return nil, err
			}
			s.st = sv.State
			s.st.fill()
			if sv.Carts != nil {
				s.carts = sv.Carts
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return s, nil
}

func emptyState() State {
	var st State
	st.fill()
	return st
}

func (st *State) fill() {
	if st.Orders == nil {
		st.Orders = []Order{}
	}
	if st.Downloads == nil {
		st.Downloads = []Download{}
	}
	if st.Forms == nil {
		st.Forms = []FormSub{}
	}
	if st.Sessions == nil {
		st.Sessions = []Session{}
	}
}

// save writes the state file; the caller holds s.mu.
func (s *Server) save() {
	if s.dir == "" {
		return
	}
	b, _ := json.MarshalIndent(saved{State: s.st, Carts: s.carts}, "", "  ")
	tmp := filepath.Join(s.dir, "state.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, filepath.Join(s.dir, "state.json"))
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	site, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	switch site {
	case "health":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "control":
		s.control(w, r)
	case "store", "bank", "card", "form":
		if path == "/"+site {
			http.Redirect(w, r, "/"+site+"/", http.StatusFound)
			return
		}
		if _, ok := walled[site]; ok && path == "/"+site+"/login" {
			s.login(w, r, site)
			return
		}
		if _, ok := walled[site]; ok && s.session(r, site) == "" {
			s.loginWall(w, r, site, "")
			return
		}
		switch site {
		case "store":
			s.store(w, r)
		case "bank", "card":
			s.bank(w, r, site)
		case "form":
			s.form(w, r)
		}
	case "":
		s.render(w, r, http.StatusOK, "index", "Fixture sites", nil)
	default:
		http.NotFound(w, r)
	}
}

// session returns the request's session token for site, or "" without a valid one.
func (s *Server) session(r *http.Request, site string) string {
	c, err := r.Cookie(cookieName(site))
	if err != nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, se := range s.st.Sessions {
		if se.Site == site && se.Token == c.Value {
			return se.Token
		}
	}
	return ""
}

// newSession records a session on site and sets its cookie.
func (s *Server) newSession(w http.ResponseWriter, site string) *http.Cookie {
	b := make([]byte, 16)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.st.Sessions = append(s.st.Sessions, Session{Site: site, Token: tok, Created: s.now().UTC()})
	s.save()
	s.mu.Unlock()
	c := &http.Cookie{Name: cookieName(site), Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}
	http.SetCookie(w, c)
	return c
}

// The fixture's test account, for signing in by hand in a browser; the suite uses /control/login.
const (
	testEmail    = "avery@example.com"
	testPassword = "fixture-password"
)

type loginData struct {
	Site, SiteName, Next, Error, Email string
}

func (s *Server) loginWall(w http.ResponseWriter, r *http.Request, site, errMsg string) {
	next := r.URL.RequestURI()
	if r.Method != http.MethodGet {
		next = "/" + site + "/"
	}
	s.render(w, r, http.StatusUnauthorized, "login", "Sign in to "+walled[site],
		loginData{Site: site, SiteName: walled[site], Next: next, Error: errMsg})
}

// login handles POST /<site>/login from the sign-in page.
func (s *Server) login(w http.ResponseWriter, r *http.Request, site string) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/"+site+"/", http.StatusFound)
		return
	}
	if r.FormValue("email") != testEmail || r.FormValue("password") != testPassword {
		d := loginData{Site: site, SiteName: walled[site], Next: r.FormValue("next"), Error: "Incorrect email or password.", Email: r.FormValue("email")}
		s.render(w, r, http.StatusUnauthorized, "login", "Sign in to "+walled[site], d)
		return
	}
	s.newSession(w, site)
	next := r.FormValue("next")
	if !strings.HasPrefix(next, "/"+site+"/") {
		next = "/" + site + "/"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// page is what every template gets: .D is the page's own data.
type page struct {
	Title string
	Site  string
	Moved bool
	D     any
}

// U is path with ?layout=moved carried on when the page is the moved variant.
func (p page) U(path string) string {
	if !p.Moved {
		return path
	}
	if strings.Contains(path, "?") {
		return path + "&layout=moved"
	}
	return path + "?layout=moved"
}

func moved(r *http.Request) bool { return r.FormValue("layout") == "moved" }

// keepLayout is the redirect target for a POST from a moved page.
func keepLayout(r *http.Request, path string) string {
	return page{Moved: moved(r)}.U(path)
}

var funcs = template.FuncMap{
	"money": func(v float64) string { return money(v) },
	"stars": func(r float64) string {
		n := int(r + 0.5)
		return strings.Repeat("★", n) + strings.Repeat("☆", 5-n)
	},
	"query":   url.QueryEscape,
	"dollars": dollars,
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, code int, name, title string, d any) {
	t := s.pages[name]
	if t == nil {
		http.Error(w, "no template "+name, http.StatusInternalServerError)
		return
	}
	site, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	var b strings.Builder
	if err := t.ExecuteTemplate(&b, "base", page{Title: title, Site: site, Moved: moved(r), D: d}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	w.Write([]byte(b.String()))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

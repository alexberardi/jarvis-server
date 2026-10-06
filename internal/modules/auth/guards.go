package auth

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

type userHandler func(w http.ResponseWriter, r *http.Request, u *user)

// bearer extracts the OAuth2PasswordBearer token: ok=false when the header is missing or
// not a Bearer scheme (FastAPI's "Not authenticated").
func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	scheme, param, _ := strings.Cut(h, " ")
	if !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	return strings.TrimSpace(param), true
}

func unauthenticated(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	detail(w, http.StatusUnauthorized, msg)
}

// currentUser is get_current_user: a valid JWT whose sub is an active user.
func (m *Module) currentUser(w http.ResponseWriter, r *http.Request) (*user, bool) {
	tok, ok := bearer(r)
	if !ok {
		unauthenticated(w, "Not authenticated")
		return nil, false
	}
	c, err := m.verify(r.Context(), tok)
	if err != nil {
		unauthenticated(w, "Could not validate credentials")
		return nil, false
	}
	id, err := c.UserID()
	if err != nil {
		unauthenticated(w, "Could not validate credentials")
		return nil, false
	}
	u, err := m.userByID(r.Context(), id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		m.internalError(w, err)
		return nil, false
	}
	if u == nil || !u.isActive {
		unauthenticated(w, "Could not validate credentials")
		return nil, false
	}
	return u, true
}

// user guards a route with get_current_user.
func (m *Module) user(h userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, ok := m.currentUser(w, r); ok {
			h(w, r, u)
		}
	}
}

// superuser guards a route with require_superuser.
func (m *Module) superuser(h userHandler) http.HandlerFunc {
	return m.user(func(w http.ResponseWriter, r *http.Request, u *user) {
		if !u.isSuperuser {
			detail(w, http.StatusForbidden, "Superuser access required")
			return
		}
		h(w, r, u)
	})
}

// admin guards /admin/* with X-Jarvis-Admin-Token (constant-time; an unset token rejects all).
func (m *Module) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authn.Equal(r.Header.Get("X-Jarvis-Admin-Token"), m.AdminToken) {
			detail(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		h(w, r)
	}
}

type appHandler func(w http.ResponseWriter, r *http.Request, a *appClient)

// app guards a route with require_app_client.
func (m *Module) app(h appHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, key := r.Header.Get("X-Jarvis-App-Id"), r.Header.Get("X-Jarvis-App-Key")
		if id == "" || key == "" {
			detail(w, http.StatusUnauthorized, "Missing app credentials")
			return
		}
		a, ok, err := m.checkApp(r, id, key)
		if err != nil {
			m.internalError(w, err)
			return
		}
		if !ok {
			detail(w, http.StatusUnauthorized, "Invalid app credentials")
			return
		}
		h(w, r, a)
	}
}

func (m *Module) checkApp(r *http.Request, id, key string) (*appClient, bool, error) {
	a, err := m.appByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !a.isActive || !m.verified.check("app", a.appID, key, a.keyHash) {
		return nil, false, nil
	}
	return a, true, nil
}

// settingsReadGuard is require_settings_auth: a superuser JWT, or app credentials.
func (m *Module) settingsReadGuard(w http.ResponseWriter, r *http.Request) bool {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return m.superuserJWT(w, r, h[len("Bearer "):])
	}
	id, key := r.Header.Get("X-Jarvis-App-Id"), r.Header.Get("X-Jarvis-App-Key")
	if id != "" && key != "" {
		_, ok, err := m.checkApp(r, id, key)
		if err != nil {
			m.internalError(w, err)
			return false
		}
		if !ok {
			detail(w, http.StatusUnauthorized, "Invalid app credentials")
		}
		return ok
	}
	detail(w, http.StatusUnauthorized, "Missing authentication. Provide either Bearer token or app credentials.")
	return false
}

// superuserJWT is _validate_superuser_jwt.
func (m *Module) superuserJWT(w http.ResponseWriter, r *http.Request, tok string) bool {
	c, err := m.verify(r.Context(), tok)
	if err != nil {
		var ve verifyError
		if !errors.As(err, &ve) {
			m.internalError(w, err)
			return false
		}
		detail(w, http.StatusUnauthorized, "Invalid token: "+ve.msg)
		return false
	}
	if c.Subject == "" {
		detail(w, http.StatusUnauthorized, "Invalid token: missing user ID")
		return false
	}
	if !c.IsSuperuser {
		detail(w, http.StatusForbidden, "Superuser access required")
		return false
	}
	id, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil {
		detail(w, http.StatusUnauthorized, "User not found or inactive")
		return false
	}
	u, err := m.userByID(r.Context(), id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		m.internalError(w, err)
		return false
	}
	if u == nil || !u.isActive {
		detail(w, http.StatusUnauthorized, "User not found or inactive")
		return false
	}
	if !u.isSuperuser {
		detail(w, http.StatusForbidden, "Superuser access required")
		return false
	}
	return true
}

// settingsWriteGuard is require_superuser.
func (m *Module) settingsWriteGuard(w http.ResponseWriter, r *http.Request) bool {
	u, ok := m.currentUser(w, r)
	if !ok {
		return false
	}
	if !u.isSuperuser {
		detail(w, http.StatusForbidden, "Superuser access required")
		return false
	}
	return true
}

// --- rate limiting (core/rate_limit.py) ---

// limited applies the per-IP flood guard (enforce_auth_rate_limit).
func (m *Module) limited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !m.limiter.cfg.Disabled && !m.limiter.checkIP(m.clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			detail(w, http.StatusTooManyRequests, "Too many requests. Please slow down.")
			return
		}
		h(w, r)
	}
}

// clientIP is the socket peer, or the right-most X-Forwarded-For hop when trusted.
func (m *Module) clientIP(r *http.Request) string {
	if m.limiter.cfg.TrustForwardedFor {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			var hops []string
			for p := range strings.SplitSeq(xff, ",") {
				if p = strings.TrimSpace(p); p != "" {
					hops = append(hops, p)
				}
			}
			if len(hops) > 0 {
				return hops[len(hops)-1]
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		if r.RemoteAddr == "" {
			return "unknown"
		}
		return r.RemoteAddr
	}
	return host
}

type window struct{ hits []time.Time }

func (w *window) count(now time.Time, span time.Duration) int {
	cut := now.Add(-span)
	w.hits = slices.DeleteFunc(w.hits, func(t time.Time) bool { return !t.After(cut) })
	return len(w.hits)
}

func (w *window) last() time.Time {
	if len(w.hits) == 0 {
		return time.Time{}
	}
	return w.hits[len(w.hits)-1]
}

type failKey struct{ email, ip string }

// rateLimiter is AuthRateLimiter: a per-IP sliding window and a per-(email, IP) failed-login
// lockout (keyed on the pair so another IP can't lock the real user out). Both maps are bounded.
type rateLimiter struct {
	cfg  RateLimit
	mu   sync.Mutex
	ip   map[string]*window
	fail map[failKey]*window
}

func newRateLimiter(cfg RateLimit) *rateLimiter {
	if cfg.IPPerMinute <= 0 {
		cfg.IPPerMinute = 30
	}
	if cfg.LoginMaxFailures <= 0 {
		cfg.LoginMaxFailures = 8
	}
	if cfg.LoginLockout <= 0 {
		cfg.LoginLockout = 900 * time.Second
	}
	if cfg.MaxKeys <= 0 {
		cfg.MaxKeys = 50_000
	}
	return &rateLimiter{cfg: cfg, ip: map[string]*window{}, fail: map[failKey]*window{}}
}

func evict[K comparable](l *rateLimiter, buckets map[K]*window, span time.Duration, t time.Time) {
	if len(buckets) < l.cfg.MaxKeys {
		return
	}
	for k, w := range buckets {
		if w.count(t, span) == 0 {
			delete(buckets, k)
		}
	}
	if len(buckets) < l.cfg.MaxKeys {
		return
	}
	type kv struct {
		k    K
		last time.Time
	}
	all := make([]kv, 0, len(buckets))
	for k, w := range buckets {
		all = append(all, kv{k, w.last()})
	}
	slices.SortFunc(all, func(a, b kv) int { return a.last.Compare(b.last) })
	for _, e := range all[:max(1, l.cfg.MaxKeys/10)] {
		delete(buckets, e.k)
	}
}

// checkIP records a request and reports whether it is under the limit.
func (l *rateLimiter) checkIP(ip string) bool {
	t := now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.ip[ip]
	if w == nil {
		evict(l, l.ip, time.Minute, t)
		w = &window{}
		l.ip[ip] = w
	}
	if w.count(t, time.Minute) >= l.cfg.IPPerMinute {
		return false
	}
	w.hits = append(w.hits, t)
	return true
}

func (l *rateLimiter) locked(email, ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.fail[failKey{strings.ToLower(email), ip}]
	return w != nil && w.count(now(), l.cfg.LoginLockout) >= l.cfg.LoginMaxFailures
}

func (l *rateLimiter) recordFailure(email, ip string) {
	t := now()
	k := failKey{strings.ToLower(email), ip}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.fail[k]
	if w == nil {
		evict(l, l.fail, l.cfg.LoginLockout, t)
		w = &window{}
		l.fail[k] = w
	}
	w.hits = append(w.hits, t)
}

func (l *rateLimiter) clearFailures(email, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fail, failKey{strings.ToLower(email), ip})
}

// --- caches ---

// graceCache holds each just-rotated refresh token's plain successor for the grace window,
// keyed by the parent row id (core/refresh_cache.py). Process-local by design.
type graceCache struct {
	mu sync.Mutex
	m  map[int64]graceEntry
}

type graceEntry struct {
	plain string
	exp   time.Time
}

func newGraceCache() *graceCache { return &graceCache{m: map[int64]graceEntry{}} }

func (g *graceCache) set(parent int64, plain string, ttl time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.m[parent] = graceEntry{plain, now().Add(ttl)}
}

func (g *graceCache) get(parent int64) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.m[parent]
	if !ok {
		return ""
	}
	if !now().Before(e.exp) {
		delete(g.m, parent)
		return ""
	}
	return e.plain
}

func (g *graceCache) delete(ids ...int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, id := range ids {
		delete(g.m, id)
	}
}

// verifiedCache remembers successful bcrypt checks of node and app keys for a short TTL, so a
// node's per-request validation doesn't pay ~100 ms of bcrypt each time. Each entry records
// the hash it was checked against: a rotated or re-registered key has a new hash and misses,
// so revocation stays instant even without an explicit invalidate (decision D49). Failures
// are never cached. Everything else validation checks (active flag, service grants,
// household members) is read fresh on every call.
type verifiedCache struct {
	ttl time.Duration
	mu  sync.Mutex
	m   map[[32]byte]verifiedEntry
}

type verifiedEntry struct {
	kind, id, hash string
	exp            time.Time
}

func newVerifiedCache(ttl time.Duration) *verifiedCache {
	return &verifiedCache{ttl: ttl, m: map[[32]byte]verifiedEntry{}}
}

func cacheKey(kind, id, secret string) [32]byte {
	return sha256.Sum256([]byte(kind + "\x00" + id + ":" + secret))
}

// check verifies secret against hash, consulting and filling the cache.
func (c *verifiedCache) check(kind, id, secret, hash string) bool {
	k := cacheKey(kind, id, secret)
	t := now()
	c.mu.Lock()
	e, ok := c.m[k]
	if ok && e.hash == hash && t.Before(e.exp) {
		c.mu.Unlock()
		return true
	}
	if ok {
		delete(c.m, k)
	}
	c.mu.Unlock()

	if !verifySecret(secret, hash) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 10_000 { // bounded: drop everything expired, else start over
		for key, e := range c.m {
			if !t.Before(e.exp) {
				delete(c.m, key)
			}
		}
		if len(c.m) > 10_000 {
			clear(c.m)
		}
	}
	c.m[k] = verifiedEntry{kind: kind, id: id, hash: hash, exp: t.Add(c.ttl)}
	return true
}

// invalidate drops every cached proof for one node or app (key rotation, deactivation,
// deletion, grant changes).
func (c *verifiedCache) invalidate(kind string, ids ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if e.kind == kind && (len(ids) == 0 || slices.Contains(ids, e.id)) {
			delete(c.m, k)
		}
	}
}

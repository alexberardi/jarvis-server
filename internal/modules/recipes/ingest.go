package recipes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes/extract"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
	"github.com/alexberardi/jarvis-server/internal/platform/ssrf"
)

// URL import (§4.3, R7): the preflight (#11), the webview payload submit (#12) and the
// recipes.ingest queue handler running the extraction chain (internal/modules/recipes/extract).
// The phone renders the page; the server never fetches recipe pages in the main path (only the
// preflight, and a server_fetch payload no client sends). r.jina.ai and scraper cookies are cut
// (RD8).

const ingestJobType = "recipes.ingest"

const (
	preflightTimeout = 3 * time.Second
	// llmReadyWait is how long an LLM pass waits for a model that is still loading.
	llmReadyWait = time.Minute
	// p1Timeout bounds one P1/P1r call (legacy: 90 s total, 80 s read).
	p1Timeout  = 90 * time.Second
	p1MaxToken = 800
	// maxIngestAttempts caps queue.max_retries (the queue handler's MaxAttempts).
	maxIngestAttempts = 10
)

// fetcher is the SSRF-guarded client for the preflight and server_fetch (tests override it).
func (m *Module) fetcher() *ssrf.Fetcher {
	if m.Fetch != nil {
		return m.Fetch
	}
	return &ssrf.Fetcher{}
}

// --- #11 preflight ---

// preflight is preflight_validate_url's result.
type preflight struct {
	ok                        bool
	status                    *int
	code, msg                 string
	nextAction, nextActionWhy string
}

func preflightFail(code, msg string) preflight { return preflight{code: code, msg: msg} }

var htmlTagRe = regexp.MustCompile(`(?i)<[a-z]+[^>]*>`)

// looksCorrupt is the printable/control test on a decoded sample (preflight and fetch).
func looksCorrupt(s string, minPrintable, maxControl float64) bool {
	sample := []rune(s)
	if len(sample) > 2000 {
		sample = sample[:2000]
	}
	if len(sample) == 0 {
		return true
	}
	printable, control := 0, 0
	for _, c := range sample {
		if (c >= 32 && c <= 126) || strings.ContainsRune(" \t\n\r\v\f\x1c\x1d\x1e\x1f\x85   ", c) {
			printable++
		}
		if c < 32 && c != '\n' && c != '\r' && c != '\t' {
			control++
		}
	}
	n := float64(len(sample))
	return float64(printable)/n < minPrintable || float64(control)/n > maxControl
}

// decodeBody decodes body by the content type's charset (UTF-8 when none). Only UTF-8 and
// Latin-1 are known; ok is false when the bytes are not valid in the charset.
func decodeBody(body []byte, ctype string) (string, bool) {
	cs := ""
	if i := strings.Index(strings.ToLower(ctype), "charset="); i >= 0 {
		cs = strings.Trim(strings.TrimSpace(strings.SplitN(ctype[i+len("charset="):], ";", 2)[0]), `"'`)
	}
	switch strings.ToLower(cs) {
	case "iso-8859-1", "latin-1", "latin1", "windows-1252", "cp1252", "us-ascii", "ascii":
		rs := make([]rune, len(body))
		for i, b := range body {
			rs[i] = rune(b)
		}
		return string(rs), true
	}
	if utf8.Valid(body) {
		return string(body), true
	}
	return "", false
}

func timeoutErr(err error) (bool, bool) {
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		return false, false
	}
	var op *net.OpError
	connecting := errors.As(err, &op) && op.Op == "dial"
	return true, connecting
}

// preflightURL is preflight_validate_url: an SSRF host check, HEAD (GET on 405), the status
// and content type, then a 5 KB GET to sniff the encoding.
func (m *Module) preflightURL(ctx context.Context, raw string) preflight {
	u, err := neturl.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return preflightFail("invalid_url", "URL must start with http or https.")
	}
	f := *m.fetcher()
	f.Timeout = preflightTimeout
	blocked := preflightFail("invalid_url", "Host is blocked (localhost/private).")
	if u.Hostname() == "" || f.IsBlockedHost(ctx, u.Hostname()) {
		return blocked
	}
	hdr := http.Header{
		"User-Agent":      {m.settings.String(ctx, SettingUserAgent, settings.Scope{})},
		"Accept":          {"text/html,application/xhtml+xml;q=0.9,*/*;q=0.8"},
		"Accept-Language": {"en-US,en;q=0.9"},
	}
	resp, err := f.Do(ctx, http.MethodHead, raw, hdr)
	if err == nil && resp.Status == http.StatusMethodNotAllowed {
		resp, err = f.Do(ctx, http.MethodGet, raw, hdr)
	}
	if err != nil {
		if errors.Is(err, ssrf.ErrBlockedHost) || errors.Is(err, ssrf.ErrInvalidURL) || errors.Is(err, ssrf.ErrTooManyRedirects) {
			return blocked
		}
		if to, connecting := timeoutErr(err); to {
			if connecting {
				return preflightFail("fetch_timeout", "Timed out connecting to the site.")
			}
			return preflightFail("fetch_timeout", "Timed out reading from the site.")
		}
		return preflightFail("fetch_failed", "Network error: "+err.Error())
	}
	ctype := resp.Header.Get("Content-Type")
	status := resp.Status
	if status >= 400 {
		p := preflightFail("fetch_failed", fmt.Sprintf("Site returned status %d.", status))
		p.status = &status
		if status == 401 || status == 403 {
			p.nextAction, p.nextActionWhy = "webview_extract", "blocked_by_site"
		}
		return p
	}
	if ctype != "" && !strings.Contains(ctype, "text/html") && !strings.Contains(ctype, "application/xhtml") {
		p := preflightFail("unsupported_content_type", "Unsupported content type: "+ctype)
		p.status = &status
		return p
	}
	if status == http.StatusOK {
		sf := f
		sf.MaxBytes = 5000
		if sample, err := sf.Do(ctx, http.MethodGet, raw, hdr); err == nil {
			p := preflight{status: &status, code: "encoding_error", nextAction: "webview_extract", nextActionWhy: "encoding_error"}
			text, ok := decodeBody(sample.Body, ctype)
			if !ok {
				p.msg = "Unable to decode HTML content with detected encoding"
				return p
			}
			if utf8.RuneCountInString(text) > 100 {
				head := text
				if rs := []rune(text); len(rs) > 2000 {
					head = string(rs[:2000])
				}
				if !htmlTagRe.MatchString(head) || looksCorrupt(text, 0.6, 0.1) {
					p.msg = "HTML content appears corrupted or has encoding issues"
					return p
				}
			}
		}
	}
	return preflight{ok: true, status: &status}
}

// httpURL validates pydantic's AnyHttpUrl and returns its normalised string (an empty path
// becomes "/"), or the error message.
func httpURL(v any) (string, string) {
	s, ok := v.(string)
	if !ok {
		return "", "URL input should be a string or URL"
	}
	u, err := neturl.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme == "" {
		return "", "Input should be a valid URL, relative URL without a base"
	}
	if sc := strings.ToLower(u.Scheme); sc != "http" && sc != "https" {
		return "", "URL scheme should be 'http' or 'https'"
	}
	if u.Hostname() == "" {
		return "", "Input should be a valid URL, empty host"
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), ""
}

// handleParseURLAsync is POST /recipes/parse-url/async (#11): the preflight, then always
// "go to the webview". The id is not a job (§8 item 7): polling it 404s.
func (m *Module) handleParseURLAsync(w http.ResponseWriter, r *http.Request, _ caller) {
	o, ok := readBody(w, r)
	if !ok {
		return
	}
	raw, present := o.m["url"]
	var target string
	if !present {
		o.fail(msgMissing, "url")
	} else if u, msg := httpURL(raw); msg != "" {
		o.fail(msg, "url")
	} else {
		target = u
	}
	o.optBool("use_llm_fallback", true)
	if !o.done(w) {
		return
	}
	jobID := newUUID()
	p := m.preflightURL(r.Context(), target)
	if !p.ok {
		detail := map[string]any{"error_code": p.code, "message": p.msg, "status_code": p.status, "job_id": jobID}
		if p.nextAction != "" {
			detail["next_action"], detail["next_action_reason"] = p.nextAction, p.nextActionWhy
		}
		httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": detail})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": jobID, "status": statusPending, "result": nil, "error_code": nil, "error_message": nil,
		"next_action": "webview_extract", "next_action_reason": "webview_required",
	})
}

// --- #12 webview payload ---

// parseInput validates IngestionInput.
func parseInput(o *obj) map[string]any {
	in, ok := o.child(o.m["input"], msgObject, "input")
	if _, present := o.m["input"]; !present {
		o.fail(msgMissing, "input")
		return nil
	}
	if !ok {
		return nil
	}
	st, present := in.m["source_type"]
	allowed := []string{"server_fetch", "client_webview", "image_upload"}
	if !present {
		in.fail(msgMissing, "source_type")
	} else if s, ok := st.(string); !ok || !slices.Contains(allowed, s) {
		in.fail(literalMsg(allowed), "source_type")
	}
	out := map[string]any{"source_type": st}
	for _, k := range []string{"source_url", "html_snippet", "extracted_at", "client"} {
		out[k] = in.optStr(k)
	}
	if blocks, ok := in.strList("jsonld_blocks", true); ok {
		out["jsonld_blocks"] = blocks
	} else {
		out["jsonld_blocks"] = nil
	}
	out["images"] = nil
	if l, ok := in.list("images", false, true); ok {
		imgs := []map[string]any{}
		for i, e := range l {
			c, ok := in.child(e, "Input should be a valid dictionary or instance of ImageRef", "images", i)
			if !ok {
				continue
			}
			imgs = append(imgs, map[string]any{
				"filename": c.str("filename"), "content_type": c.optStr("content_type"), "data_base64": c.str("data_base64"),
			})
		}
		out["images"] = imgs
	}
	return out
}

// handleParsePayloadAsync is POST /recipes/parse-payload/async (#12): a parse job for the
// payload (job_data is the validated input with every key), stamped with the household.
func (m *Module) handleParsePayloadAsync(w http.ResponseWriter, r *http.Request, c caller) {
	o, ok := readBody(w, r)
	if !ok {
		return
	}
	input := parseInput(o)
	if !o.done(w) {
		return
	}
	ctx := r.Context()
	var id string
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = m.createJob(ctx, tx, c, jobTypeIngestion, ingestJobType, input)
		return err
	})
	if err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Queue.Notify(ingestJobType)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": id, "status": statusPending})
}

// --- the recipes.ingest handler ---

// label resolves a model setting to an LLM label, warning on a non-label value.
func (m *Module) label(ctx context.Context, key string) string {
	v := m.settings.String(ctx, key, settings.Scope{})
	if !strings.EqualFold(v, llm.LabelLive) && !strings.EqualFold(v, llm.LabelBackground) {
		m.deps.Log.Warn("recipes: model setting is not a label; using live", "key", key, "value", v)
	}
	return llm.NormalizeLabel(v)
}

// chatJSON runs one JSON-mode chat on label and returns the reply content.
func (m *Module) chatJSON(ctx context.Context, label string, temp float64, maxTokens int, timeout time.Duration,
	system, user string) (string, error) {
	if m.LLM == nil {
		return "", errors.New("LLM unavailable")
	}
	cctx, cancel := context.WithTimeout(llm.WithReadyWait(ctx, llmReadyWait), llmReadyWait+timeout)
	defer cancel()
	resp, err := m.LLM.Chat(cctx, llm.ChatRequest{
		Label: label, Temperature: &temp, MaxTokens: &maxTokens,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Messages: []llm.Message{
			{Role: "system", Content: llm.TextContent(system)},
			{Role: "user", Content: llm.TextContent(user)},
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	return resp.Content, nil
}

// p1Chat is the extractor's LLM: P1 and P1r on llm.full_model_name.
func (m *Module) p1Chat(ctx context.Context, system, user string) (string, error) {
	return m.chatJSON(ctx, m.label(ctx, SettingFullModel), 0, p1MaxToken, p1Timeout, system, user)
}

func (m *Module) runIngest(ctx context.Context, qj queue.Job) ([]byte, error) {
	j, run, err := m.claimJob(ctx, qj)
	if err != nil || !run {
		return nil, err
	}
	var in extract.Input
	if err := json.Unmarshal([]byte(j.JobData.String), &in); err != nil {
		return nil, m.markError(ctx, j.ID, "invalid_payload", err.Error())
	}
	res, err := extract.Ingest(ctx, in, m.p1Chat, m.fetchHTML)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		m.deps.Log.Error("recipes: ingest job crashed", "parse_job_id", j.ID, "job_type", j.JobType, "err", err)
		return nil, m.markError(ctx, j.ID, "worker_error", err.Error())
	}
	if res.Success {
		m.deps.Log.Info("recipes: ingest job done", "parse_job_id", j.ID, "job_type", j.JobType, "outcome", "complete",
			"strategy", *res.ParserStrategy)
		return nil, m.markComplete(ctx, j.ID, extract.JobResult(res))
	}
	code, msg := "parse_failed", "Parse failed"
	if res.ErrorCode != nil && *res.ErrorCode != "" {
		code = *res.ErrorCode
	}
	if res.ErrorMessage != nil && *res.ErrorMessage != "" {
		msg = *res.ErrorMessage
	}
	encoding := code == "fetch_failed" && (slices.Contains(res.Warnings, "encoding_error") || res.NextAction != nil)
	maxRetries := min(max(int(m.settings.Int(ctx, SettingMaxRetries, settings.Scope{})), 1), maxIngestAttempts)
	attempts, err := m.jobAttempts(ctx, j.ID)
	if err != nil {
		return nil, err
	}
	if !encoding && res.NextAction == nil && attempts < maxRetries &&
		(code == "llm_timeout" || code == "llm_failed" || code == "fetch_failed") {
		m.deps.Log.Warn("recipes: ingest job will retry", "parse_job_id", j.ID, "job_type", j.JobType,
			"error_code", code, "attempt", attempts, "max", maxRetries)
		now := ts(m.now())
		if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE recipes_recipe_parse_jobs SET status = 'PENDING',
			error_code = ?, error_message = ?, updated_at = ? WHERE id = ? AND status = 'RUNNING'`, code, msg, now, j.ID); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("recipes: ingest %s: %s", code, msg)
	}
	m.deps.Log.Info("recipes: ingest job done", "parse_job_id", j.ID, "job_type", j.JobType, "outcome", code)
	if res.NextAction != nil {
		// Legacy stored the result so the client can see the suggestion, then forced ERROR.
		if err := m.markComplete(ctx, j.ID, extract.JobResult(res)); err != nil {
			return nil, err
		}
		_, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE recipes_recipe_parse_jobs SET status = 'ERROR', error_code = ?,
			error_message = ? WHERE id = ? AND status = 'COMPLETE'`, code, msg, j.ID)
		return nil, err
	}
	return nil, m.markError(ctx, j.ID, code, msg)
}

func (m *Module) jobAttempts(ctx context.Context, id string) (int, error) {
	var n int
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT attempts FROM recipes_recipe_parse_jobs WHERE id = ?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// fetchHTML is fetch_html for a server_fetch payload: one GET (retried with Accept: */* on
// 401/403), HTML or text only, decoded and sanity-checked. RD8: no r.jina.ai fallback, no
// cookies; B12: the UA is the scraper.user_agent setting.
func (m *Module) fetchHTML(ctx context.Context, url string) (string, *extract.Result) {
	f := *m.fetcher()
	f.Timeout = 15 * time.Second
	hdr := http.Header{
		"User-Agent":      {m.settings.String(ctx, SettingUserAgent, settings.Scope{})},
		"Accept":          {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
		"Accept-Language": {"en-US,en;q=0.9"},
		"Referer":         {"https://www.google.com/"},
	}
	fail := func(r extract.Result) (string, *extract.Result) { return "", &r }
	resp, err := f.Do(ctx, http.MethodGet, url, hdr)
	if err == nil && (resp.Status == 401 || resp.Status == 403) {
		hdr.Set("Accept", "*/*")
		resp, err = f.Do(ctx, http.MethodGet, url, hdr)
	}
	if err != nil {
		if errors.Is(err, ssrf.ErrBlockedHost) || errors.Is(err, ssrf.ErrInvalidURL) || errors.Is(err, ssrf.ErrTooManyRedirects) {
			return fail(extract.Fail("invalid_url", err.Error()))
		}
		return fail(extract.Fail("fetch_failed", err.Error(), "fetch_http_error"))
	}
	if resp.Status >= 400 {
		r := extract.Fail("fetch_failed", fmt.Sprintf("status_%d", resp.Status), "fetch_http_error")
		if resp.Status == 401 || resp.Status == 403 {
			na, why := "webview_extract", "blocked_by_site"
			r.Warnings, r.NextAction, r.NextActionReason = []string{"blocked_by_site"}, &na, &why
		}
		return fail(r)
	}
	ctype := resp.Header.Get("Content-Type")
	if !strings.Contains(ctype, "text/html") && !strings.Contains(ctype, "text/plain") {
		return fail(extract.Fail("invalid_url", "Unsupported content type: "+ctype))
	}
	text, ok := decodeBody(resp.Body, ctype)
	if ok && utf8.RuneCountInString(text) > 100 {
		head := text
		if rs := []rune(text); len(rs) > 2000 {
			head = string(rs[:2000])
		}
		if htmlTagRe.MatchString(head) && !looksCorrupt(text, 0.6000001, 0.1) {
			return text, nil
		}
	}
	na, why := "webview_extract", "encoding_error"
	r := extract.Fail("fetch_failed", "HTML content appears corrupted or invalid encoding", "encoding_error")
	r.NextAction, r.NextActionReason = &na, &why
	return fail(r)
}

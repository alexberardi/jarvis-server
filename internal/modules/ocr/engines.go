package ocr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // DecodeConfig for bbox sizing
	_ "image/jpeg" // DecodeConfig for bbox sizing
	_ "image/png"  // DecodeConfig for bbox sizing
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Engine names, as /v1/providers and the request's `provider` field spell them.
const (
	EngineTesseract   = "tesseract"
	EngineAppleVision = "apple_vision"
	EngineLLMVision   = "llm_proxy_vision"
)

// Image is one input image.
type Image struct {
	Data        []byte
	ContentType string
}

// Options are the per-request OCR options (legacy OCROptions).
type Options struct {
	LanguageHints []string `json:"language_hints"`
	ReturnBoxes   bool     `json:"return_boxes"`
	Mode          string   `json:"mode"`
}

// Block is a recognised text region: bbox is [x, y, width, height] in pixels.
type Block struct {
	Text       string    `json:"text"`
	BBox       []float64 `json:"bbox"`
	Confidence float64   `json:"confidence"`
}

// Result is one engine's reading of one image.
type Result struct {
	Text   string
	Blocks []Block
}

// Engine is an OCR backend.
type Engine interface {
	Name() string
	// Available reports whether the engine can take work now. It must be cheap: it is asked
	// on every request (remote engines cache their probe).
	Available(ctx context.Context) bool
	Recognize(ctx context.Context, img Image, o Options) (Result, error)
}

// Diagnostic is one /v1/providers diagnostics entry.
type Diagnostic struct {
	Provider  string  `json:"provider"`
	Available bool    `json:"available"`
	Reason    string  `json:"reason"` // ok, unavailable, unreachable, auth_failed, not_configured, unhealthy, probe_error
	Detail    *string `json:"detail"`
}

// Diagnoser is implemented by engines that can say why they are unavailable.
type Diagnoser interface {
	Diagnose(ctx context.Context) Diagnostic
}

func diagnose(ctx context.Context, e Engine) Diagnostic {
	if d, ok := e.(Diagnoser); ok {
		return d.Diagnose(ctx)
	}
	ok := e.Available(ctx)
	reason := "unavailable"
	if ok {
		reason = "ok"
	}
	return Diagnostic{Provider: e.Name(), Available: ok, Reason: reason}
}

func strp(s string) *string { return &s }

// imageSize returns the pixel size of a PNG, JPEG or GIF; ok is false for anything else.
func imageSize(data []byte) (w, h int, ok bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// --- tesseract (exec) ---

// Tesseract runs the tesseract CLI. It is optional: jarvisd registers it only when the binary
// is on PATH (or configured).
type Tesseract struct {
	Path string
}

func (t *Tesseract) Name() string                   { return EngineTesseract }
func (t *Tesseract) Available(context.Context) bool { return t.Path != "" }

// tessLangs maps the two-letter hints callers send to tesseract's codes (legacy lang_map).
var tessLangs = map[string]string{"en": "eng", "fr": "fra", "de": "deu", "es": "spa", "it": "ita"}

func tessLang(hints []string) string {
	if len(hints) == 0 {
		return "eng"
	}
	if len(hints) > 3 {
		hints = hints[:3]
	}
	out := make([]string, 0, len(hints))
	for _, h := range hints {
		h = strings.ToLower(h)
		if m, ok := tessLangs[h]; ok {
			h = m
		}
		out = append(out, h)
	}
	return strings.Join(out, "+")
}

func (t *Tesseract) run(ctx context.Context, img []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, t.Path, append([]string{"stdin", "stdout"}, args...)...)
	cmd.Stdin = bytes.NewReader(img)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("tesseract failed: %s", msg)
	}
	return stdout.Bytes(), nil
}

func (t *Tesseract) Recognize(ctx context.Context, img Image, o Options) (Result, error) {
	if t.Path == "" {
		return Result{}, errors.New("Tesseract is not available")
	}
	lang := tessLang(o.LanguageHints)
	out, err := t.run(ctx, img.Data, "-l", lang)
	if err != nil {
		return Result{}, err
	}
	res := Result{Text: strings.TrimSpace(string(out)), Blocks: []Block{}}
	if o.ReturnBoxes {
		tsv, err := t.run(ctx, img.Data, "-l", lang, "tsv")
		if err != nil {
			return Result{}, err
		}
		res.Blocks = parseTSV(tsv)
	}
	return res, nil
}

// parseTSV reads tesseract's TSV output (pytesseract's image_to_data): one row per word,
// confidence 0-100 (-1 for non-word rows).
func parseTSV(b []byte) []Block {
	blocks := []Block{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue // header
		}
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 12 {
			continue
		}
		text := strings.TrimSpace(f[11])
		if text == "" {
			continue
		}
		num := func(s string) float64 { v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return v }
		conf := num(f[10])
		if conf < 0 {
			conf = 0
		} else {
			conf /= 100
		}
		blocks = append(blocks, Block{Text: text, BBox: []float64{num(f[6]), num(f[7]), num(f[8]), num(f[9])}, Confidence: conf})
	}
	return blocks
}

// --- Apple Vision through jarvis-osx-api ---

// AppleVision reaches Apple Vision on a Mac through jarvis-osx-api (POST /v1/ocr, Bearer an
// ocr:read service key), the legacy remote_ocr provider. Its availability changes minute to
// minute, so it is probed per request with a short-TTL cache, and an auth failure is reported
// apart from an unreachable host.
type AppleVision struct {
	URL, Key string
	Client   *http.Client
	ProbeTTL time.Duration // default 30 s

	mu      sync.Mutex
	probeAt time.Time
	ok      bool
	reason  string
	detail  *string
	now     func() time.Time
}

const normalizedBBox = "normalized_xywh_topleft"

func (a *AppleVision) Name() string { return EngineAppleVision }

func (a *AppleVision) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return http.DefaultClient
}

func (a *AppleVision) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *AppleVision) configured() bool { return a.URL != "" && a.Key != "" }

func (a *AppleVision) base() string { return strings.TrimRight(a.URL, "/") }

func (a *AppleVision) mark(ok bool, reason string, detail *string) {
	a.mu.Lock()
	a.ok, a.reason, a.detail, a.probeAt = ok, reason, detail, a.clock()
	a.mu.Unlock()
}

func (a *AppleVision) Available(ctx context.Context) bool { return a.Diagnose(ctx).Available }

func (a *AppleVision) Diagnose(ctx context.Context) Diagnostic {
	if !a.configured() {
		return Diagnostic{Provider: a.Name(), Reason: "not_configured"}
	}
	ttl := a.ProbeTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	a.mu.Lock()
	fresh := !a.probeAt.IsZero() && a.clock().Sub(a.probeAt) < ttl
	d := Diagnostic{Provider: a.Name(), Available: a.ok, Reason: a.reason, Detail: a.detail}
	a.mu.Unlock()
	if fresh {
		return d
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, a.base()+"/health", nil)
	if err != nil {
		a.mark(false, "probe_error", strp(err.Error()))
	} else {
		req.Header.Set("Authorization", "Bearer "+a.Key)
		resp, err := a.client().Do(req)
		switch {
		case err != nil:
			a.mark(false, "unreachable", strp(err.Error()))
		default:
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			switch {
			case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
				a.mark(false, "auth_failed", strp(fmt.Sprintf("probe returned %d", resp.StatusCode)))
			case resp.StatusCode >= 400:
				a.mark(false, "unhealthy", strp(fmt.Sprintf("probe returned %d", resp.StatusCode)))
			default:
				a.mark(true, "ok", nil)
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return Diagnostic{Provider: a.Name(), Available: a.ok, Reason: a.reason, Detail: a.detail}
}

func (a *AppleVision) Recognize(ctx context.Context, img Image, o Options) (Result, error) {
	if !a.configured() {
		return Result{}, errors.New("apple_vision is not configured (needs the jarvis-osx-api URL and key)")
	}
	hints := o.LanguageHints
	if len(hints) == 0 {
		hints = []string{"en"}
	}
	body, _ := json.Marshal(map[string]any{
		"image_base64":   base64.StdEncoding.EncodeToString(img.Data),
		"language_hints": hints, "return_boxes": o.ReturnBoxes, "mode": o.Mode,
	})
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, a.base()+"/v1/ocr", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+a.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client().Do(req)
	if err != nil {
		a.mark(false, "unreachable", strp(err.Error()))
		return Result{}, fmt.Errorf("apple_vision unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		a.mark(false, "auth_failed", strp(fmt.Sprintf("request returned %d", resp.StatusCode)))
		return Result{}, fmt.Errorf("apple_vision credentials rejected (%d): check the ocr:read key", resp.StatusCode)
	case resp.StatusCode >= 400:
		a.mark(false, "unhealthy", strp(fmt.Sprintf("request returned %d", resp.StatusCode)))
		return Result{}, fmt.Errorf("apple_vision failed (%d): %s", resp.StatusCode, truncRunes(string(data), 200))
	}
	var out struct {
		Text   string `json:"text"`
		Format string `json:"bbox_format"`
		Blocks []struct {
			Text       string    `json:"text"`
			BBox       []float64 `json:"bbox"`
			Confidence float64   `json:"confidence"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return Result{}, fmt.Errorf("apple_vision returned invalid JSON: %w", err)
	}
	a.mark(true, "ok", nil)
	res := Result{Text: out.Text, Blocks: []Block{}}
	if o.ReturnBoxes && len(out.Blocks) > 0 {
		sx, sy := 1.0, 1.0
		if out.Format == normalizedBBox {
			if w, h, ok := imageSize(img.Data); ok {
				sx, sy = float64(w), float64(h)
			}
		}
		for _, b := range out.Blocks {
			bb := append([]float64{}, b.BBox...)
			for len(bb) < 4 {
				bb = append(bb, 0)
			}
			res.Blocks = append(res.Blocks, Block{Text: b.Text, BBox: []float64{bb[0] * sx, bb[1] * sy, bb[2] * sx, bb[3] * sy}, Confidence: b.Confidence})
		}
	}
	return res, nil
}

// --- LLM vision through an OpenAI-compatible endpoint ---

// LLMVision asks a vision model to transcribe the image (legacy LLMProxyVisionProvider, model
// "background"). During the strangler phase the endpoint is the legacy llm-proxy.
type LLMVision struct {
	URL, AppID, AppKey string
	Model              string // default "background"
	Client             *http.Client
	// TimeoutFn is the per-image timeout (the module reads ocr.llm_vision_timeout_seconds);
	// nil: defaultLLMVisionTimeout.
	TimeoutFn func(context.Context) time.Duration
}

// defaultLLMVisionTimeout is 180 s: the legacy 60 s timed out on a dense page with a 9B model
// sharing its one slot with recipes' structuring call (A10e M4).
const defaultLLMVisionTimeout = 180 * time.Second

func (l *LLMVision) timeout(ctx context.Context) time.Duration {
	if l.TimeoutFn != nil {
		if d := l.TimeoutFn(ctx); d > 0 {
			return d
		}
	}
	return defaultLLMVisionTimeout
}

func (l *LLMVision) Name() string { return EngineLLMVision }

func (l *LLMVision) Available(context.Context) bool {
	return l.URL != "" && l.AppID != "" && l.AppKey != ""
}

const llmOCRPrompt = `OCR this image and extract all text. Return the result as JSON in this exact format:
{
  "page1": {
    "text": "extracted text here"
  }
}

The text field should contain all readable text from the image. If the image contains no text, return an empty string.`

func (l *LLMVision) Recognize(ctx context.Context, img Image, o Options) (Result, error) {
	if !l.Available(ctx) {
		return Result{}, errors.New("LLM vision is not configured")
	}
	prompt := llmOCRPrompt
	if len(o.LanguageHints) > 0 {
		prompt += " The text may be in: " + strings.Join(o.LanguageHints, ", ") + "."
	}
	ct := img.ContentType
	if ct == "" {
		ct = "image/png"
	}
	model := l.Model
	if model == "" {
		model = "background"
	}
	content, err := chatCompletion(ctx, l.Client, l.URL, l.AppID, l.AppKey, l.timeout(ctx), map[string]any{
		"model": model,
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": prompt},
			map[string]any{"type": "image_url", "image_url": map[string]any{
				"url": "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(img.Data)}},
		}}},
		"max_tokens":      4096,
		"response_format": map[string]any{"type": "json_object"},
	})
	if err != nil {
		return Result{}, err
	}
	text := content
	var parsed struct {
		Page1 struct {
			Text string `json:"text"`
		} `json:"page1"`
	}
	if json.Unmarshal([]byte(content), &parsed) == nil {
		text = parsed.Page1.Text
	}
	res := Result{Text: text, Blocks: []Block{}}
	if o.ReturnBoxes {
		w, h, _ := imageSize(img.Data)
		res.Blocks = append(res.Blocks, Block{Text: text, BBox: []float64{0, 0, float64(w), float64(h)}, Confidence: 0.95})
	}
	return res, nil
}

// chatCompletion posts an OpenAI-compatible chat request with app credentials and returns
// the first choice's content, trimmed.
func chatCompletion(ctx context.Context, client *http.Client, baseURL, appID, appKey string, timeout time.Duration, body any) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Jarvis-App-Id", appID)
	req.Header.Set("X-Jarvis-App-Key", appKey)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to reach LLM endpoint: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("LLM endpoint returned %d: %s", resp.StatusCode, truncRunes(string(data), 200))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return "", errors.New("invalid response format from LLM endpoint")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

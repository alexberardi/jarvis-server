package phone

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Provider is the telephony vendor seam (gateway telephony/provider.py, PRD decision 7): place
// a call whose media is streamed to our WebSocket, end it, and look up a number's line type.
// The Media Streams wire format (TwiML, frames, signatures) lives in package live. Tests use
// a fake; only Twilio below talks to a real vendor.
type Provider interface {
	// StartCall places the outbound call with the given stream instructions (TwiML); it
	// returns the provider's call id.
	StartCall(ctx context.Context, to, instructions string) (string, error)
	// EndCall terminates the call server-side.
	EndCall(ctx context.Context, callSID string) error
	// LineType is "mobile" | "landline" | "voip" | "unknown"; any failure is "unknown".
	LineType(ctx context.Context, e164 string) string
}

// Twilio is the Twilio REST provider (telephony/twilio_provider.py, services/line_lookup.py).
// Credentials come from the environment, never the settings DB.
type Twilio struct {
	AccountSID string
	AuthToken  string
	FromNumber string
	// APIBase / LookupBase default to Twilio's endpoints (overridable for a local fake).
	APIBase    string
	LookupBase string
	Client     *http.Client
}

func (t *Twilio) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (t *Twilio) apiBase() string {
	if t.APIBase != "" {
		return strings.TrimRight(t.APIBase, "/")
	}
	return "https://api.twilio.com/2010-04-01"
}

func (t *Twilio) post(ctx context.Context, path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.apiBase()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(t.AccountSID, t.AuthToken)
	resp, err := t.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("twilio %s: HTTP %d", path, resp.StatusCode)
	}
	return body, nil
}

// StartCall is calls.create with inline TwiML (bidirectional streams need <Connect><Stream>).
func (t *Twilio) StartCall(ctx context.Context, to, instructions string) (string, error) {
	body, err := t.post(ctx, "/Accounts/"+url.PathEscape(t.AccountSID)+"/Calls.json",
		url.Values{"To": {to}, "From": {t.FromNumber}, "Twiml": {instructions}})
	if err != nil {
		return "", err
	}
	var out struct {
		SID string `json:"sid"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.SID == "" {
		return "", fmt.Errorf("twilio calls.create: no sid in response")
	}
	return out.SID, nil
}

// EndCall sets the call's status to completed.
func (t *Twilio) EndCall(ctx context.Context, callSID string) error {
	_, err := t.post(ctx, "/Accounts/"+url.PathEscape(t.AccountSID)+"/Calls/"+url.PathEscape(callSID)+".json",
		url.Values{"Status": {"completed"}})
	return err
}

// LineType is Lookup v2 line_type_intelligence; errors and missing credentials are "unknown".
func (t *Twilio) LineType(ctx context.Context, e164 string) string {
	if t.AccountSID == "" || t.AuthToken == "" {
		return "unknown"
	}
	base := t.LookupBase
	if base == "" {
		base = "https://lookups.twilio.com/v2/PhoneNumbers"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(base, "/")+"/"+url.PathEscape(e164)+"?Fields=line_type_intelligence", nil)
	if err != nil {
		return "unknown"
	}
	req.SetBasicAuth(t.AccountSID, t.AuthToken)
	resp, err := t.client().Do(req)
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "unknown"
	}
	var out struct {
		LTI struct {
			Type string `json:"type"`
		} `json:"line_type_intelligence"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out) != nil || out.LTI.Type == "" {
		return "unknown"
	}
	// Raw Twilio vocabulary ("nonFixedVoip" etc.); the plan keeps only mobile/landline/voip and
	// maps the rest to "unknown", as legacy CC did.
	return out.LTI.Type
}

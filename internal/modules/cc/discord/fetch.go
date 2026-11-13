package discord

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// AllowedFetch says whether an attachment URL is one the bridge will download: https on
// Discord's own file hosts only, so a message cannot make the server fetch anything else.
func AllowedFetch(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	switch u.Hostname() {
	case "cdn.discordapp.com", "media.discordapp.net":
		return true
	}
	return false
}

// FetchAttachment downloads an attachment, refusing other hosts and anything over max bytes.
func FetchAttachment(ctx context.Context, raw string, max int64) ([]byte, error) {
	if !AllowedFetch(raw) {
		return nil, errors.New("discord: attachment host not allowed")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{CheckRedirect: func(r *http.Request, _ []*http.Request) error {
		if !AllowedFetch(r.URL.String()) {
			return errors.New("discord: redirect to a host that is not allowed")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discord: attachment download: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errors.New("discord: attachment too large")
	}
	return data, nil
}

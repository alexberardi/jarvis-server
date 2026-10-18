package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// applySettings puts a scenario's household settings (Scenario.Settings) through the app's
// API as the primary household admin, so a scenario that needs a feature switched on (say
// watches.enabled) runs the same by hand and in a full run on a minimally seeded sandbox. It
// returns a restore that puts the earlier values back.
func (d *Driver) applySettings(ctx context.Context, want map[string]any) (func(context.Context) error, error) {
	hdr, hh, err := d.primaryHousehold(ctx)
	if err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	base := d.Node.BaseURL + "/api/v0/mobile/household/" + url.PathEscape(hh) + "/settings"
	var cur struct {
		Settings map[string]any `json:"settings"`
	}
	if err := getJSON(ctx, base, hdr, &cur); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var done []string
	restore := func(ctx context.Context) error {
		var errs []string
		for _, k := range done {
			if err := putSetting(ctx, base, k, cur.Settings[k], hdr); err != nil {
				errs = append(errs, err.Error())
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("restore settings: %s", strings.Join(errs, "; "))
		}
		return nil
	}
	for _, k := range keys {
		if _, ok := cur.Settings[k]; !ok {
			_ = restore(ctx)
			return nil, fmt.Errorf("settings: %s is not a household setting", k)
		}
		if err := putSetting(ctx, base, k, want[k], hdr); err != nil {
			_ = restore(ctx)
			return nil, fmt.Errorf("settings: %w", err)
		}
		done = append(done, k)
	}
	return restore, nil
}

// putSetting PUTs {"value": v} to base/key and wants a 200.
func putSetting(ctx context.Context, base, key string, v any, header map[string]string) error {
	b, err := json.Marshal(map[string]any{"value": v})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/"+key, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("PUT %s: %d %s", req.URL.Path, resp.StatusCode, truncate(strings.TrimSpace(string(rb)), 500))
	}
	return nil
}

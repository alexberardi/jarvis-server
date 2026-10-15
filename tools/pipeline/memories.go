package main

import (
	"context"
	"fmt"
	"net/url"
)

// clearMemories deletes the primary user's own memories through the app's API, so a
// "fresh_memories" scenario starts with nothing stored: what one run's remember saved would
// otherwise reach the next run's prompt as a profile match, and the model answers "I already
// know that" instead of calling the tool.
func (d *Driver) clearMemories(ctx context.Context) error {
	hdr, hh, err := d.primaryHousehold(ctx)
	if err != nil {
		return fmt.Errorf("fresh_memories: %w", err)
	}
	base := d.Node.BaseURL + "/api/v0/mobile/memories"
	q := "household_id=" + url.QueryEscape(hh)
	var mems []struct {
		ID int64 `json:"id"`
	}
	if err := getJSON(ctx, base+"?"+q+"&include_household=false", hdr, &mems); err != nil {
		return fmt.Errorf("fresh_memories: %w", err)
	}
	for _, m := range mems {
		if err := doDelete(ctx, fmt.Sprintf("%s/%d?%s", base, m.ID, q), hdr); err != nil {
			return fmt.Errorf("fresh_memories: %w", err)
		}
	}
	return nil
}

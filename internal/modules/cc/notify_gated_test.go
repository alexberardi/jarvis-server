package cc

import (
	"context"
	"strconv"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
)

func TestNotifyGated(t *testing.T) {
	ctx := context.Background()
	uid := int64(7)
	note := func(src string) gatedNote {
		return gatedNote{HouseholdID: sigHH, UserID: &uid, Source: src, Category: "watch", Title: "Leak", Summary: "Water in the basement",
			Body: "Sensor 3 reads wet.", Metadata: map[string]any{"watch_id": "w1"}, DedupeKey: src + "-k", Push: true}
	}
	pushes := func(se *sigEnv) []notifications.Notification {
		se.notify.mu.Lock()
		defer se.notify.mu.Unlock()
		return append([]notifications.Notification(nil), se.notify.pushes...)
	}

	t.Run("attention off", func(t *testing.T) {
		se := newSigEnv(t)
		id, by := se.m.notifyGated(ctx, note("off"))
		if id == "" || by != "" {
			t.Fatal(id, by)
		}
		items := se.items()
		if len(items) != 1 || items[0].Metadata["watch_id"] != "w1" || items[0].Body != "Sensor 3 reads wet." || *items[0].UserID != uid {
			t.Fatal(items)
		}
		p := pushes(se)
		if len(p) != 1 || p[0].TargetType != "user" || p[0].TargetID != strconv.FormatInt(uid, 10) || p[0].Data["inbox_item_id"] != id {
			t.Fatal(p)
		}
	})

	t.Run("household target", func(t *testing.T) {
		se := newSigEnv(t)
		n := note("hh")
		n.UserID = nil
		if id, _ := se.m.notifyGated(ctx, n); id == "" {
			t.Fatal("not delivered")
		}
		p := pushes(se)
		if len(p) != 1 || p[0].TargetType != "household" || p[0].TargetID != sigHH {
			t.Fatal(p)
		}
	})

	t.Run("dedupe", func(t *testing.T) {
		se := newSigEnv(t)
		se.createNode("n1", sigHH)
		se.set(settingAttention, true, sigHH)
		se.set(settingAttnQuietHours, "", sigHH)
		if id, by := se.m.notifyGated(ctx, note("dup")); id == "" || by != "" {
			t.Fatal(id, by)
		}
		if id, by := se.m.notifyGated(ctx, note("dup")); id != "" || by != "dedupe" {
			t.Fatal(id, by)
		}
		if len(se.items()) != 1 || len(pushes(se)) != 1 {
			t.Fatal(se.items(), pushes(se))
		}
	})

	t.Run("quiet hours", func(t *testing.T) {
		se := newSigEnv(t)
		se.createNode("n1", sigHH)
		se.set(settingAttention, true, sigHH)
		se.set(settingAttnQuietHours, "11:00-13:00", sigHH)
		n := note("quiet")
		n.Category = "other"
		if id, by := se.m.notifyGated(ctx, n); id == "" || by != "" {
			t.Fatal(id, by)
		}
		if len(se.items()) != 1 || len(pushes(se)) != 0 {
			t.Fatal(se.items(), pushes(se))
		}
	})

	t.Run("inbox only", func(t *testing.T) {
		se := newSigEnv(t)
		n := note("inbox")
		n.Push = false
		if id, _ := se.m.notifyGated(ctx, n); id == "" {
			t.Fatal("not delivered")
		}
		if len(se.items()) != 1 || len(pushes(se)) != 0 {
			t.Fatal(se.items(), pushes(se))
		}
	})
}

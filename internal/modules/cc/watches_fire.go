package cc

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/watches"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

// Watch firing (docs/autonomy/PLAN.md §2 "Firing"): an observation is checked against the
// watch's condition and fire policy, and a met condition posts the watch-alert card through
// notifyGated. The card's Pause and Stop buttons are server callbacks ("watch.<callback>").
const (
	watchCheckJob = "cc.watch_check"
	watchSweepJob = "cc.watch_sweep"

	watchCallbackCommand = "watch"
)

func watchTrigger(id string) string { return "watch:" + id }

// armWatch (re)puts a watch's next check. A failure is logged.
func (m *Module) armWatch(ctx context.Context, w watches.Watch, at time.Time) {
	if m.deps.Scheduler == nil {
		return
	}
	payload, _ := json.Marshal(map[string]string{"watch_id": w.ID})
	if err := m.deps.Scheduler.Put(ctx, scheduler.Trigger{
		Name: watchTrigger(w.ID), Kind: scheduler.KindOnce, JobType: watchCheckJob,
		Spec: scheduler.Spec{At: at}, Payload: payload,
	}); err != nil {
		m.deps.Log.Error("cc: arm watch", "id", w.ID, "err", err)
	}
}

func (m *Module) disarmWatch(ctx context.Context, id string) {
	if m.deps.Scheduler == nil {
		return
	}
	if err := m.deps.Scheduler.Delete(ctx, watchTrigger(id)); err != nil {
		m.deps.Log.Error("cc: disarm watch", "id", id, "err", err)
	}
}

// watchObservation is one check's result: the value read, or the error that stopped the read.
type watchObservation struct {
	Value, Err, SourceURL string
	At                    time.Time
}

// observeWatch records obs on w and fires the watch when its condition is met and its fire
// policy allows. It reports whether it fired. w is the watch as it was before this check.
func (m *Module) observeWatch(ctx context.Context, w watches.Watch, obs watchObservation) bool {
	if obs.Err != "" {
		if err := m.watches.RecordCheck(ctx, w.ID, "", obs.Err); err != nil {
			m.deps.Log.Warn("cc: watch check not recorded", "id", w.ID, "err", err)
		}
		return false
	}
	prev := w.LastValue
	met := watches.Eval(w.Condition, obs.Value, prev)
	if err := m.watches.RecordCheck(ctx, w.ID, obs.Value, ""); err != nil {
		m.deps.Log.Warn("cc: watch check not recorded", "id", w.ID, "err", err)
	}
	if !met || !fireAllowed(w, obs.Value, m.now()) {
		return false
	}
	return m.fireWatch(ctx, w, prev, obs, alertSummary(prev, obs.Value))
}

// fireAllowed applies the watch's fire policy to a met condition (w is the watch before this check).
func fireAllowed(w watches.Watch, value string, now time.Time) bool {
	switch w.FirePolicy {
	case watches.FireEveryChange:
		return w.LastFiredAt == nil || normValue(value) != normValue(w.LastValue)
	case watches.FireCooldown:
		return w.LastFiredAt == nil || now.Sub(*w.LastFiredAt) >= time.Duration(w.CooldownSeconds)*time.Second
	default: // once
		return w.LastFiredAt == nil
	}
}

// normValue is the watches package's comparison form: lower case, single spaces.
func normValue(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// fireWatch posts the alert card, records the fire (a once watch is done) and the autonomy event.
func (m *Module) fireWatch(ctx context.Context, w watches.Watch, prev string, obs watchObservation, summary string) bool {
	state := watches.StateActive
	if w.FirePolicy == watches.FireOnce {
		state = watches.StateDone
	}
	loc := m.householdLocation(ctx, w.HouseholdID)
	m.notifyGated(ctx, gatedNote{HouseholdID: w.HouseholdID, UserID: w.UserID, Source: "watch", Category: "watch_alert",
		Title: "👀 " + w.Title, Summary: summary, Body: alertBody(w, obs, summary, m.observedAt(obs), loc),
		Metadata: watchAlertMetadata(w, prev, obs, state, m.observedAt(obs)), DedupeKey: "watch:" + w.ID + ":" + shortHash(obs.Value),
		Push: true})
	if err := m.watches.RecordFire(ctx, w.ID, state); err != nil {
		m.deps.Log.Warn("cc: watch fire not recorded", "id", w.ID, "err", err)
	}
	if state == watches.StateDone {
		m.disarmWatch(ctx, w.ID)
	}
	m.recordAutonomy(ctx, policy.Event{HouseholdID: w.HouseholdID, UserID: w.UserID, Source: policy.SourceWatch, SourceID: w.ID,
		Effect: policy.Allow, Outcome: policy.OutcomeFired, Reason: "the watch's condition was met",
		Detail: map[string]any{"value": obs.Value, "previous": prev}})
	return true
}

func (m *Module) observedAt(obs watchObservation) time.Time {
	if obs.At.IsZero() {
		return m.now()
	}
	return obs.At
}

// alertSummary is "<prev> → <value>" when the value moved, else "Now: <value>".
func alertSummary(prev, value string) string {
	if prev != "" && prev != value {
		return prev + " → " + value
	}
	return "Now: " + value
}

// watchURL is the page the observation came from, else the watch's own page ("" for a search
// or a signal).
func watchURL(w watches.Watch, obs watchObservation) string {
	if obs.SourceURL != "" {
		return obs.SourceURL
	}
	return w.Source.URL
}

func alertBody(w watches.Watch, obs watchObservation, summary string, at time.Time, loc *time.Location) string {
	var b strings.Builder
	b.WriteString("Watching for " + watches.Describe(w) + ".\n\n")
	b.WriteString(summary + "\n")
	b.WriteString("Checked " + at.In(loc).Format(time.RFC1123) + ".")
	if u := watchURL(w, obs); u != "" {
		label := watches.SourceLabel(u)
		if label == "" {
			label = "the page"
		}
		b.WriteString("\n[Open " + label + "](" + u + ")")
	}
	return b.String()
}

// watchAlertMetadata is the watch-alert card (blueprint Interfaces "Card metadata"): the Pause and
// Stop buttons only while the watch stays active.
func watchAlertMetadata(w watches.Watch, prev string, obs watchObservation, state string, at time.Time) map[string]any {
	md := map[string]any{"household_id": w.HouseholdID, "card_type": "watch_alert", "watch_id": w.ID, "state": state,
		"looking_for": watches.Describe(w), "value": obs.Value, "previous": nil,
		"checked_at": at.UTC().Format(time.RFC3339), "source_url": nil, "source_label": ""}
	if prev != "" {
		md["previous"] = prev
	}
	if u := watchURL(w, obs); u != "" {
		md["source_url"] = u
		md["source_label"] = watches.SourceLabel(u)
	}
	if state == watches.StateActive {
		md["interactive_elements"] = []any{
			watchElement("pause-watch", "Pause", "pause_watch", w.ID),
			watchElement("stop-watch", "Stop watching", "cancel_watch", w.ID),
		}
	}
	return md
}

func watchElement(id, label, callback, watchID string) map[string]any {
	return map[string]any{"id": id, "label": label, "command": watchCallbackCommand, "callback": callback,
		"target": "server", "data": map[string]any{"watch_id": watchID}}
}

// --- callbacks: Pause, Resume, Stop watching ---

func (m *Module) watchCallbacks() map[string]serverCallbackFunc {
	return map[string]serverCallbackFunc{
		callbackKey(watchCallbackCommand, "pause_watch"):  m.pauseWatchTap,
		callbackKey(watchCallbackCommand, "resume_watch"): m.resumeWatchTap,
		callbackKey(watchCallbackCommand, "cancel_watch"): m.cancelWatchTap,
	}
}

// tappedWatch is the household's watch the tap names; nil (and Success) when it is gone.
func (m *Module) tappedWatch(ctx context.Context, c callbackContext) (*watches.Watch, error) {
	id, _ := c.Data["watch_id"].(string)
	if id == "" || m.watches == nil {
		return nil, nil
	}
	return m.watches.Get(ctx, c.HouseholdID, id)
}

func watchReply(hh, title, summary string, elems ...map[string]any) callbackResult {
	md := map[string]any{"household_id": hh}
	if len(elems) > 0 {
		list := make([]any, len(elems))
		for i, e := range elems {
			list[i] = e
		}
		md["interactive_elements"] = list
	}
	return callbackResult{Success: true, ContextData: map[string]any{"inbox": map[string]any{
		"title": title, "summary": summary, "metadata": md}}}
}

func (m *Module) pauseWatchTap(ctx context.Context, c callbackContext) (callbackResult, error) {
	w, err := m.tappedWatch(ctx, c)
	if err != nil || w == nil {
		return callbackResult{Success: true}, err
	}
	switch w.State {
	case watches.StatePaused:
		return watchReply(c.HouseholdID, "Watch already paused", "“"+w.Title+"” is already paused.",
			watchElement("resume-watch", "Resume", "resume_watch", w.ID),
			watchElement("stop-watch", "Stop watching", "cancel_watch", w.ID)), nil
	case watches.StateDone:
		return watchReply(c.HouseholdID, "Already stopped watching", "I'm already not checking “"+w.Title+"”."), nil
	}
	if _, err := m.watches.SetState(ctx, c.HouseholdID, w.ID, watches.StatePaused); err != nil {
		return callbackResult{}, err
	}
	m.disarmWatch(ctx, w.ID)
	return watchReply(c.HouseholdID, "⏸️ Watch paused", "I'll stop checking “"+w.Title+"” until you resume it.",
		watchElement("resume-watch", "Resume", "resume_watch", w.ID),
		watchElement("stop-watch", "Stop watching", "cancel_watch", w.ID)), nil
}

func (m *Module) resumeWatchTap(ctx context.Context, c callbackContext) (callbackResult, error) {
	w, err := m.tappedWatch(ctx, c)
	if err != nil || w == nil {
		return callbackResult{Success: true}, err
	}
	switch w.State {
	case watches.StateActive:
		return watchReply(c.HouseholdID, "Watch already running", "I'm already checking “"+w.Title+"”."), nil
	case watches.StateDone:
		return watchReply(c.HouseholdID, "Already stopped watching", "I'm no longer checking “"+w.Title+"”."), nil
	}
	if _, err := m.watches.SetState(ctx, c.HouseholdID, w.ID, watches.StateActive); err != nil {
		return callbackResult{}, err
	}
	if w.SourceKind == watches.SourceWeb {
		m.armWatch(ctx, *w, m.now())
	}
	return watchReply(c.HouseholdID, "▶️ Watch resumed", "I'll keep checking “"+w.Title+"”."), nil
}

func (m *Module) cancelWatchTap(ctx context.Context, c callbackContext) (callbackResult, error) {
	w, err := m.tappedWatch(ctx, c)
	if err != nil || w == nil {
		return callbackResult{Success: true}, err
	}
	if w.State == watches.StateDone {
		return watchReply(c.HouseholdID, "Already stopped watching", "I'm already not checking “"+w.Title+"”."), nil
	}
	if _, err := m.watches.SetState(ctx, c.HouseholdID, w.ID, watches.StateDone); err != nil {
		return callbackResult{}, err
	}
	m.disarmWatch(ctx, w.ID)
	return watchReply(c.HouseholdID, "🛑 Stopped watching", "I won't check “"+w.Title+"” any more."), nil
}

// sweepWatches ends every watch whose expiry has come and drops its next check.
func (m *Module) sweepWatches(ctx context.Context) error {
	ids, err := m.watches.ExpireDue(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		m.disarmWatch(ctx, id)
	}
	if len(ids) > 0 {
		m.deps.Log.Info("cc: watches expired", "count", len(ids))
	}
	return nil
}

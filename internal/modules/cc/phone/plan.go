package phone

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/fuzzy"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// Plan creation (phone_call_service.create_call_plan): caps → phonebook → DNC → web search →
// line type → LLM brief → availability → call context → prior context → draft row → confirm
// card. The rule is "never vanish": every early exit posts a card.

// checkCaps returns a refusal when a cap blocks a new plan or call. Any error blocks
// (fail closed). Caps are counted in UTC, as legacy (oddity 17).
func (s *Service) checkCaps(ctx context.Context, hh string) string {
	refusal, err := s.caps(ctx, hh)
	if err != nil {
		s.log().Error("phone: caps check failed — blocking (fail-closed)", "err", err)
		return "Phone calls are temporarily unavailable."
	}
	return refusal
}

func (s *Service) caps(ctx context.Context, hh string) (string, error) {
	now := s.now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	q := s.DB.Read
	var today, active, used int64
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_phone_call_sessions
		WHERE household_id = ? AND created_at >= ?`, hh, dbTime(dayStart)).Scan(&today); err != nil {
		return "", err
	}
	if today >= s.intSetting(ctx, SettingCallsPerDay, hh, 10) {
		return "The daily call limit for this household has been reached.", nil
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_phone_call_sessions
		WHERE household_id = ? AND state IN ('dialing', 'in_call', 'wrapup')`, hh).Scan(&active); err != nil {
		return "", err
	}
	if active >= s.intSetting(ctx, SettingMaxConcurrent, hh, 1) {
		return "There's already a call in progress for this household.", nil
	}
	// D40 11.Q6: duration_seconds is measured locally from in_call_at, so this cap counts.
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(duration_seconds), 0) FROM cc_phone_call_sessions
		WHERE household_id = ? AND created_at >= ? AND duration_seconds IS NOT NULL`, hh, dbTime(monthStart)).Scan(&used); err != nil {
		return "", err
	}
	if used >= s.intSetting(ctx, SettingMonthlyMinutes, hh, 60)*60 {
		return "The monthly call-minutes limit for this household has been reached.", nil
	}
	return "", nil
}

// PlanRequest asks for a call plan. ErrandID/ErrandStep link it to a workflow run.
type PlanRequest struct {
	Business     string
	Goal         string
	HouseholdID  string
	UserID       *int64
	ErrandID     string
	ErrandStep   *int64
	PriorContext string
}

// CreatePlan drafts a call plan and posts its confirm card, returning the session id. It
// returns "" (with a card already posted) when the call is refused before any session exists:
// a cap, or a do-not-call contact. A number miss still drafts a session (the user can type
// the number), so an errand suspends rather than fails. Errands call this directly.
func (s *Service) CreatePlan(ctx context.Context, req PlanRequest) string {
	id, err := s.createPlan(ctx, req)
	if err != nil {
		s.log().Error("phone: call plan creation failed", "err", err)
		s.postCard(ctx, card{HouseholdID: req.HouseholdID, UserID: req.UserID,
			Title: "📵 Call plan failed", Summary: "Couldn't prepare the call: " + err.Error(),
			Metadata: hhMeta(req.HouseholdID)})
		return ""
	}
	return id
}

// planAsync is the tool's background plan (legacy loop.create_task).
func (s *Service) planAsync(req PlanRequest) {
	s.planWG.Add(1)
	go func() {
		defer s.planWG.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx()), 5*time.Minute)
		defer cancel()
		s.CreatePlan(ctx, req)
	}()
}

func (s *Service) createPlan(ctx context.Context, req PlanRequest) (string, error) {
	hh := req.HouseholdID
	if refusal := s.checkCaps(ctx, hh); refusal != "" {
		s.postCard(ctx, card{HouseholdID: hh, UserID: req.UserID, Title: "📵 Call not started",
			Summary: refusal, Metadata: hhMeta(hh)})
		return "", nil
	}

	contact, score := s.resolveContact(ctx, hh, req.Business)
	resolved, lineType := "", "unknown"
	if contact != nil {
		if contact.DoNotCall {
			s.postCard(ctx, card{HouseholdID: hh, UserID: req.UserID, Title: "📵 Call refused",
				Summary:  contact.Name + " is marked do-not-call for this household.",
				Body:     "Remove the do-not-call flag from the phonebook to call them again.",
				Metadata: hhMeta(hh)})
			return "", nil
		}
		if n, err := NormalizeUS(contact.Number); err == nil {
			resolved = n
		}
		if contact.LineType != nil && *contact.LineType != "" {
			lineType = *contact.LineType
		}
	}

	numberSource := ""
	if resolved != "" {
		numberSource = "phonebook"
	}
	var searchAddress, searchURL, locationWarning, resolutionNote string
	if contact == nil {
		res := s.findBusinessNumber(ctx, req.Business, hh)
		if res.Number != "" {
			resolved, numberSource = res.Number, "web"
			searchAddress, searchURL = res.Address, res.SourceURL
			locationWarning = LocationMismatch(res.Address, res.SearchedNear)
			near := ""
			if res.SearchedNear != "" {
				near = " (searched near " + res.SearchedNear + ")"
			}
			resolutionNote = "I found this number via web search" + near + " — check it before calling."
		} else {
			resolutionNote = searchMissNote(res.Reason)
		}
	}
	if numberSource == "phonebook" {
		resolutionNote = "This number is from your phonebook."
	}
	if contact != nil && score < 95 {
		// D40 11.Q5: make a fuzzy substitution visible; the confirm tap is the real guard.
		resolutionNote += fmt.Sprintf(" I matched **%s** for '%s' — make sure it's the business you meant.",
			contact.Name, req.Business)
	}

	if resolved != "" && lineType == "unknown" {
		// The lookup bills the household's own account; none configured leaves it unknown.
		if tel, err := s.Telephony(ctx, hh); err == nil {
			lctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			lineType = normalizeLineType(tel.Provider.LineType(lctx, resolved))
			cancel()
		}
	}

	initiator := s.initiatorName(ctx, req.UserID)
	details := s.draftDetails(ctx, req.Business, req.Goal, initiator)
	details = s.applyAvailability(ctx, hh, req.Goal, details, req.UserID)
	if block := BuildContextBlock(SelectForCall(s.loadCallContext(ctx, req.UserID))); block != "" {
		details = strings.TrimRight(details, " \t\n\r\v\f") + "\n\n" + block
	}
	if req.PriorContext != "" {
		details = strings.TrimRight(details, " \t\n\r\v\f") + "\n\n" + req.PriorContext
	}

	now := s.now()
	ttl := s.intSetting(ctx, SettingPlanTTL, hh, 20)
	sess := &Session{
		ID: uuid4(), HouseholdID: hh, UserID: req.UserID, ContactName: req.Business, Goal: req.Goal,
		Details: details, ResolvedNumber: resolved, LineType: lineType, State: StateDraft,
		ErrandID: req.ErrandID, ErrandStep: req.ErrandStep, CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(ttl) * time.Minute), ContactAddress: searchAddress,
	}
	if contact != nil {
		sess.ContactID, sess.ContactName = contact.ID, contact.Name
		if contact.Address != nil && *contact.Address != "" {
			sess.ContactAddress = *contact.Address
		}
	}
	if _, err := s.DB.Write.ExecContext(ctx, `INSERT INTO cc_phone_call_sessions
		(id, household_id, user_id, contact_id, contact_name, contact_address, goal, details, resolved_number,
		 line_type, state, errand_id, errand_step, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'draft', ?, ?, ?, ?)`,
		sess.ID, hh, nullInt(req.UserID), nullStr(sess.ContactID), sess.ContactName, nullStr(sess.ContactAddress),
		sess.Goal, sess.Details, nullStr(resolved), lineType, nullStr(req.ErrandID), nullInt(req.ErrandStep),
		dbTime(now), dbTime(sess.ExpiresAt)); err != nil {
		return "", err
	}

	mobileNote := ""
	if lineType == "mobile" {
		mobileNote = " Note: this appears to be a mobile number."
	}
	sourceLine := ""
	if searchURL != "" {
		sourceLine = " Source: " + searchURL
	}
	warningLine := ""
	if locationWarning != "" {
		warningLine = locationWarning + "\n\n"
	}
	if numberSource == "" {
		numberSource = "none"
	}
	meta := sessMeta(sess)
	meta["number_source"] = numberSource
	meta["editor_schema"] = 2
	meta["editable_fields"] = []any{
		map[string]any{"label": "Phone number", "initial": resolved, "data_key": "dialed_number", "input_type": "tel", "required": true},
		map[string]any{"label": "Details", "initial": details, "data_key": "details", "input_type": "multiline", "required": true},
	}
	meta["expires_at"] = sess.ExpiresAt.Format(cardTimeFmt)
	meta["interactive_elements"] = []any{
		map[string]any{"id": "confirm-call", "label": "Call now", "command": ToolName, "callback": CallbackConfirm,
			"target": "server", "data": map[string]any{"session_id": sess.ID, "dialed_number": "", "details": ""}},
		map[string]any{"id": "cancel-call", "label": "Cancel", "command": ToolName, "callback": CallbackCancel,
			"target": "server", "data": map[string]any{"session_id": sess.ID}},
	}
	s.postCard(ctx, card{
		HouseholdID: hh, UserID: req.UserID,
		Title:   "📞 Call plan: " + sess.ContactName,
		Summary: req.Goal + mobileNote,
		Body: warningLine + "Review the number and details, then tap **Call now**. " +
			resolutionNote + sourceLine + " The call will open with an AI + recording disclosure." + mobileNote,
		Metadata: meta,
	})
	return sess.ID, nil
}

func normalizeLineType(lt string) string {
	switch lt {
	case "mobile", "landline", "voip":
		return lt
	}
	return "unknown"
}

// resolveContact is resolve_contact: WRatio@80 over the household's contacts, ties going to
// the first by name (D40 11.Q5: deterministic). Best effort: any error is a miss.
func (s *Service) resolveContact(ctx context.Context, hh, business string) (*Contact, float64) {
	cs, err := s.contacts(ctx, hh)
	if err != nil {
		s.log().Warn("phone: contact lookup failed", "err", err)
		return nil, 0
	}
	if len(cs) == 0 {
		return nil, 0
	}
	choices := make([]fuzzy.Choice, len(cs))
	byID := map[string]*Contact{}
	for i, c := range cs {
		choices[i] = fuzzy.Choice{Key: c.ID, Text: c.NormalizedName}
		byID[c.ID] = c
	}
	best, score, ok := fuzzy.ExtractOne(NormalizeName(business), choices, 80)
	if !ok {
		return nil, 0
	}
	return byID[best.Key], score
}

func searchMissNote(reason string) string {
	switch reason {
	case "web_search_disabled":
		return "This business isn't in your phonebook, and web search is off " +
			"for your household, so I couldn't look it up — enter the " +
			"number to call."
	case "no_results", "no_number_found":
		return "This business isn't in your phonebook and I couldn't find a " +
			"number for it online — enter the number to call."
	case "search_failed":
		return "This business isn't in your phonebook and the lookup failed — " +
			"enter the number to call."
	}
	return "I couldn't find this business in the phonebook — enter the number to call."
}

// numberSearch is NumberSearchResult.
type numberSearch struct {
	Number, SourceURL, Address, Reason, SearchedNear string
}

const searchMaxPages = 3

// findBusinessNumber is find_business_number: gated on web_search.enabled, biased toward the
// household location, the top 3 pages scraped through the SSRF-guarded fetcher, the first
// valid number wins. D40 11.Q4: numbers on the household's do-not-call list are skipped.
func (s *Service) findBusinessNumber(ctx context.Context, business, hh string) numberSearch {
	if !s.boolSetting(ctx, settingWebSearch, hh) {
		return numberSearch{Reason: "web_search_disabled"}
	}
	if s.Search == nil {
		return numberSearch{Reason: "search_failed"}
	}
	loc := s.location(ctx, hh)
	query := business + " phone number"
	if loc != "" {
		query = business + " " + loc + " phone number"
	}
	// _search_web asks for 2 results (quick_search's _NUM_RESULTS) and swallows errors into
	// an empty list, so a provider failure reads as "no results", as legacy.
	results, err := s.Search.Search(ctx, query, 2)
	if err != nil {
		s.log().Warn("phone: number search failed", "err", err)
	}
	if len(results) == 0 {
		return numberSearch{Reason: "no_results"}
	}
	if len(results) > searchMaxPages {
		results = results[:searchMaxPages]
	}
	skip, err := s.dncNumbers(ctx, hh)
	if err != nil {
		return numberSearch{Reason: "search_failed"}
	}
	fetch := s.Fetch
	if fetch == nil {
		fetch = &servertools.Fetcher{}
	}
	hdr := http.Header{"User-Agent": {"Mozilla/5.0 (compatible; JarvisBot/1.0)"}}
	for _, r := range results {
		content := ""
		if status, body, err := fetch.Get(ctx, r.URL, hdr); err == nil && status == http.StatusOK {
			content = servertools.ExtractText(strings.ToValidUTF8(string(body), "�"))
			if len([]rune(content)) > 4000 {
				content = string([]rune(content)[:4000])
			}
		}
		if content == "" {
			content = r.Snippet
		}
		if n := ExtractNumber(content, skip); n != "" {
			return numberSearch{Number: n, SourceURL: r.URL, Address: ExtractAddress(content), SearchedNear: loc}
		}
	}
	return numberSearch{Reason: "no_number_found"}
}

func (s *Service) initiatorName(ctx context.Context, uid *int64) string {
	if uid == nil || s.Names == nil {
		return ""
	}
	names, err := s.Names.UserNames(ctx, []int64{*uid})
	if err != nil {
		return ""
	}
	return names[*uid]
}

// draftSystemPrompt is _draft_details's system prompt (tuned live; byte-for-byte).
const draftSystemPrompt = "You draft briefs for an assistant that places phone " +
	"calls to businesses. The assistant can use ONLY what " +
	"the brief contains — it cannot look anything up " +
	"mid-call — so gather everything the business will " +
	"plausibly need up front. Format: 1-3 plain sentences " +
	"stating exactly what to accomplish, then short " +
	"'If asked:' lines anticipating the business's " +
	"questions (name for the order/booking, quantities, " +
	"sizes, timing). Use the caller's name when given. " +
	"For appointments or anything needing scheduling, end " +
	"with an 'Acceptable times:' line — copy times from " +
	"the goal if stated, otherwise write exactly " +
	"'Acceptable times: (fill in your availability " +
	"before calling)'. Never include payment details. " +
	"Stay strictly consistent with the goal: never mix " +
	"pickup and delivery; only include an address for a " +
	"delivery; invent no facts."

// draftDetails drafts the brief on the background slot with reasoning off; it degrades to
// the raw goal.
func (s *Service) draftDetails(ctx context.Context, business, goal, initiator string) string {
	if s.LLM == nil {
		return ensureTimesSection(goal, goal)
	}
	user := "Business: " + business + "\nGoal: " + goal
	if initiator != "" {
		user += "\nCaller name: " + initiator
	}
	temp, maxTok, budget := 0.3, 200, 0
	resp, err := s.LLM.Chat(ctx, llm.ChatRequest{
		Label: "background", Temperature: &temp, MaxTokens: &maxTok, ReasoningBudget: &budget,
		Messages: []llm.Message{
			{Role: "system", Content: llm.TextContent(draftSystemPrompt)},
			{Role: "user", Content: llm.TextContent(user)},
		},
	})
	if err != nil || resp == nil {
		s.log().Warn("phone: details draft failed, using raw goal", "err", err)
		return ensureTimesSection(goal, goal)
	}
	content := strings.TrimSpace(resp.Content)
	if content == "" {
		content = goal
	}
	return ensureTimesSection(goal, content)
}

var schedulingHints = []string{"appointment", "book", "schedule", "reservation", "reserve", "visit"}

// isSchedulingGoal: does this call need times negotiated on it?
func isSchedulingGoal(goal, details string) bool {
	l := strings.ToLower(goal + " " + details)
	for _, h := range schedulingHints {
		if strings.Contains(l, h) {
			return true
		}
	}
	return false
}

// ensureTimesSection: scheduling goals always carry an Acceptable-times line to fill in.
func ensureTimesSection(goal, details string) string {
	if strings.Contains(strings.ToLower(details), "acceptable times") || !isSchedulingGoal(goal, details) {
		return details
	}
	return strings.TrimRight(details, " \t\n\r\v\f") + "\nAcceptable times: (fill in your availability before calling)"
}

var constraintPrefixes = []string{"acceptable times:", "do not book:"}

// ExtractConstraintEnvelope pulls the negotiating bounds back out of the confirmed brief
// (placeholders are not constraints).
func ExtractConstraintEnvelope(details string) string {
	if details == "" {
		return ""
	}
	var real []string
	for _, line := range splitLines(details) {
		t := strings.TrimSpace(line)
		lt := strings.ToLower(t)
		match := false
		for _, p := range constraintPrefixes {
			if strings.HasPrefix(lt, p) {
				match = true
			}
		}
		if match && !strings.Contains(lt, "(fill in") {
			real = append(real, t)
		}
	}
	return strings.Join(real, "\n")
}

var lineSplitRE = regexp.MustCompile(`\r\n|[\n\r\v\f\x1c\x1d\x1e\x{85}\x{2028}\x{2029}]`)

// splitLines is Python's str.splitlines (no trailing empty line).
func splitLines(s string) []string {
	parts := lineSplitRE.Split(s, -1)
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func formatAvailability(free, busy []string) string {
	clean := func(xs []string) []string {
		var out []string
		for _, x := range xs {
			if strings.TrimSpace(x) != "" {
				out = append(out, x)
			}
		}
		if len(out) > 6 {
			out = out[:6]
		}
		return out
	}
	free, busy = clean(free), clean(busy)
	if len(free) == 0 && len(busy) == 0 {
		return ""
	}
	parts := []string{}
	if len(free) > 0 {
		parts = append(parts, "Acceptable times: "+strings.Join(free, "; "))
	} else {
		parts = append(parts, "Acceptable times: (calendar shows no free windows — edit me)")
	}
	if len(busy) > 0 {
		parts = append(parts, "Do not book: "+strings.Join(busy, "; "))
	}
	return strings.Join(parts, "\n")
}

func stripTimesPlaceholder(details string) string {
	var kept []string
	for _, line := range splitLines(details) {
		l := strings.ToLower(line)
		if strings.HasPrefix(l, "acceptable times:") && (strings.Contains(l, "fill in") || strings.Contains(l, "unavailable")) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

var errNoCalendar = errors.New("no availability provider")

// applyAvailability bakes real calendar availability into a scheduling brief at plan time
// (the call itself never gets a calendar). Degrades to the placeholder in every direction.
func (s *Service) applyAvailability(ctx context.Context, hh, goal, details string, uid *int64) string {
	if !isSchedulingGoal(goal, details) {
		return details
	}
	l := strings.ToLower(details)
	if strings.Contains(l, "acceptable times") && !strings.Contains(l, "(fill in") {
		return details
	}
	var ans AvailabilityAnswer
	err := errNoCalendar
	if s.Calendar != nil {
		day := s.now().Truncate(24 * time.Hour)
		ans, err = s.Calendar.Availability(ctx, hh, uid, day, day.AddDate(0, 0, 7))
	}
	if err != nil {
		return ensureTimesSection(goal, details)
	}
	if !ans.OK {
		return strings.TrimRight(stripTimesPlaceholder(details), " \t\n\r\v\f") +
			"\nAcceptable times: (calendar unavailable — fill in your availability)"
	}
	env := formatAvailability(ans.Free, ans.Busy)
	if env == "" {
		return ensureTimesSection(goal, details)
	}
	return strings.TrimRight(stripTimesPlaceholder(details), " \t\n\r\v\f") + "\n" + env
}

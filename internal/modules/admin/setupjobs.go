package admin

import (
	"slices"

	llmmod "github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// The per-job setup summary (AD3b): the wizard has one step per model job, its Done step and
// the Models page show a checklist of them, and the dashboard names what voice still lacks.
// A job is ready when its label runs a model, and under way while an install that will assign
// that label downloads: labels are assigned only when the install finishes, so the label state
// alone would call a downloading job missing.

// Job states.
const (
	JobReady       = "ready"       // the label runs a model (or reaches a remote one)
	JobLoading     = "loading"     // a model is assigned and its engine is starting
	JobDownloading = "downloading" // an install that will assign the label is queued or running
	JobFailed      = "failed"      // the label's engine or its latest install failed
	JobMissing     = "missing"     // nothing assigned and nothing on the way
)

// setupJob is one model job: the label that decides its state, any labels installed with it,
// and whether a voice turn needs it.
type setupJob struct {
	ID       string
	Label    string
	Extra    []string
	Required bool
}

// setupJobs are the wizard's model steps in order: Language model, Speech-to-text, Voice,
// Voice recognition, Memory & search. The ids are the wizard's step names.
var setupJobs = []setupJob{
	{ID: "llm", Label: "live", Extra: []string{"background"}, Required: true},
	{ID: "stt", Label: "stt", Required: true},
	{ID: "voice", Label: "tts", Required: true},
	{ID: "speaker", Label: "speaker"},
	{ID: "memory", Label: "embeddings"},
}

// JobInstall is the install a job waits on (or whose failure it reports).
type JobInstall struct {
	ID         int64  `json:"id"`
	ModelID    string `json:"model_id"`
	State      string `json:"state"`
	Phase      string `json:"phase"`
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
	Error      string `json:"error,omitempty"`
}

// JobSummary is one row of the setup checklist.
type JobSummary struct {
	Job        string      `json:"job"`
	Label      string      `json:"label"`
	Labels     []string    `json:"labels"`
	Required   bool        `json:"required"`
	State      string      `json:"state"`
	LabelState string      `json:"label_state"`
	Install    *JobInstall `json:"install,omitempty"`
}

// labelFailed are label states that need the operator, not patience.
var labelFailed = []string{"failed", "no_engine_build", "misconfigured", "error"}

func isActive(state string) bool { return state == "queued" || state == "running" }

// summarizeJobs folds the label states and the recent installs (newest first) into the
// per-job checklist.
func summarizeJobs(labels map[string]string, installs []llmmod.SetupInstall) []JobSummary {
	out := make([]JobSummary, 0, len(setupJobs))
	for _, j := range setupJobs {
		ls := labels[j.Label]
		if ls == "" {
			ls = llmmod.StateNotConfigured
		}
		s := JobSummary{Job: j.ID, Label: j.Label, Labels: append([]string{j.Label}, j.Extra...),
			Required: j.Required, LabelState: ls}
		// The newest install that assigns this label, and whether one is still under way.
		var latest, active *llmmod.SetupInstall
		for i := range installs {
			if !slices.Contains(installs[i].Assign, j.Label) {
				continue
			}
			if latest == nil {
				latest = &installs[i]
			}
			if active == nil && isActive(installs[i].State) {
				active = &installs[i]
			}
		}
		switch {
		case slices.Contains(liveStates, ls):
			s.State = JobReady
		case active != nil:
			s.State, s.Install = JobDownloading, jobInstall(active)
		case ls == llmmod.StateNotConfigured:
			s.State = JobMissing
			if latest != nil && latest.State == "failed" {
				s.State, s.Install = JobFailed, jobInstall(latest)
			}
		case slices.Contains(labelFailed, ls):
			s.State = JobFailed
		default:
			s.State = JobLoading
		}
		out = append(out, s)
	}
	return out
}

func jobInstall(i *llmmod.SetupInstall) *JobInstall {
	return &JobInstall{ID: i.ID, ModelID: i.ModelID, State: i.State, Phase: i.Phase,
		BytesDone: i.BytesDone, BytesTotal: i.BytesTotal, Error: i.Error}
}

// chosen says the operator has dealt with a job: it runs, loads, downloads or failed trying.
func chosen(s JobSummary) bool { return s.State != JobMissing }

// wizardSteps are the steps a signed-in superuser can resume at, in order (AD3, AD3a, AD3b).
var wizardSteps = []string{"hardware", "llm", "stt", "voice", "speaker", "memory", "privacy", "done"}

// setupStep is where a signed-in superuser resumes the wizard, from the install itself rather
// than one tab's storage (A10 F9): "" once it was finished; else the step the wizard last
// recorded (admin setting setup.step), and without one, the first required job nobody has
// started (Hardware while the language model is missing), then Privacy.
func setupStep(completed bool, saved string, jobs []JobSummary) string {
	if completed {
		return ""
	}
	if slices.Contains(wizardSteps, saved) {
		return saved
	}
	for _, j := range jobs {
		if !j.Required || chosen(j) {
			continue
		}
		if j.Job == "llm" {
			return "hardware"
		}
		return j.Job
	}
	return "privacy"
}

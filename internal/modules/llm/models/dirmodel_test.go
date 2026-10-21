package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

var (
	mac   = engine.Platform{OS: "darwin", Arch: "arm64"}
	linux = engine.Platform{OS: "linux", Arch: "amd64"}
)

func TestInferDirModel(t *testing.T) {
	for _, c := range []struct {
		name, config string
		want         DirTraits
	}{
		{"Qwen3.5-9B-4bit", `{"model_type":"qwen3_5"}`, DirTraits{"qwen3.5", 9, "Qwen3_5_9B_Compressed", true}},
		{"Qwen3.5-27B-4bit", `{"architectures":["Qwen3_5ForConditionalGeneration"]}`, DirTraits{"qwen3.5", 27, "Qwen3_14B_Compressed", true}},
		{"Qwen3.5-35B-A3B-4bit", `{"model_type":"qwen3_5_moe"}`, DirTraits{"qwen3.5", 35, "Qwen3_14B_Compressed", true}},
		{"Qwen3.8-27B-4bit", ``, DirTraits{"qwen3.5", 27, "Qwen3_14B_Compressed", true}},
		{"Qwen3.5-9B-4bit", ``, DirTraits{"qwen3.5", 9, "Qwen3_5_9B_Compressed", true}},
		{"Qwen3-8B-4bit", `{"model_type":"qwen3"}`, DirTraits{"qwen3", 8, "Qwen3_8B_Compressed", false}},
		{"Qwen3-4B-MLX-bf16", `{"architectures":["Qwen3ForCausalLM"]}`, DirTraits{"qwen3", 4, "Qwen3_8B_Compressed", false}},
		{"Qwen3-14B-4bit", `{"model_type":"qwen3"}`, DirTraits{"qwen3", 14, "Qwen3_14B_Compressed", false}},
		{"Qwen3-30B-A3B-4bit", `{"model_type":"qwen3_moe"}`, DirTraits{"qwen3", 30, "Qwen3_14B_Compressed", false}},
		// No size in the name: from the config (40 layers of 5120: about 16B).
		{"my-finetune", `{"model_type":"qwen3","hidden_size":5120,"num_hidden_layers":40,"intermediate_size":17408,"vocab_size":151936}`,
			DirTraits{"qwen3", 16.4, "Qwen3_14B_Compressed", false}},
		// The config wins over the name.
		{"Qwen3-8B-abliterated", `{"model_type":"llama"}`, DirTraits{"", 8, "", false}},
		{"Llama-3.1-8B-Instruct-4bit", `{"model_type":"llama"}`, DirTraits{"", 8, "", false}},
	} {
		got := InferDirModel(c.name, []byte(c.config))
		got.ParamsB = float64(int(got.ParamsB*10)) / 10
		if got != c.want {
			t.Errorf("%s %s: %+v, want %+v", c.name, c.config, got, c.want)
		}
	}
}

func TestChoicesDirectory(t *testing.T) {
	r := Repo{ID: "mlx-community/Qwen3.5-9B-4bit", Files: []RepoFile{
		{Name: "model-00002-of-00002.safetensors", Size: 40},
		{Name: "model-00001-of-00002.safetensors", Size: 60},
		{Name: "model.safetensors.index.json", Size: 3},
		{Name: "tokenizer.json", Size: 5},
		{Name: "tokenizer_config.json", Size: 4},
		{Name: "chat_template.jinja", Size: 1},
		{Name: "config.json", Size: 2},
		{Name: "README.md", Size: 7},
		{Name: ".gitattributes", Size: 7},
		{Name: "images/x.png", Size: 7},
		{Name: "original/consolidated.safetensors", Size: 500},
		{Name: "modeling_qwen.py", Size: 7},
	}}
	cs := Choices(r)
	if len(cs) != 1 {
		t.Fatalf("choices %+v", cs)
	}
	c := cs[0]
	var names []string
	for _, f := range c.Shards {
		names = append(names, f.Name)
	}
	want := []string{"config.json", "chat_template.jinja", "model-00001-of-00002.safetensors", "model-00002-of-00002.safetensors",
		"model.safetensors.index.json", "tokenizer.json", "tokenizer_config.json"}
	if !c.Directory || c.File != DirChoiceFile || c.Kind != "llm" || c.Quant != "4bit" || c.Size != 115 ||
		c.PromptProvider != "Qwen3_5_9B_Compressed" || !slices.Equal(names, want) {
		t.Fatalf("%+v\n%v", c, names)
	}
	// Any file of the folder installs the folder; a GGUF beside it stays its own choice.
	r.Files = append(r.Files, RepoFile{Name: "Q.gguf", Size: 9})
	if c, err := choiceFor(r, DirChoiceFile); err != nil || !c.Directory {
		t.Fatalf("%+v %v", c, err)
	}
	if c, err := choiceFor(r, "Q.gguf"); err != nil || c.Directory || len(c.Shards) != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	// A config without safetensors (a GGUF repo's) is no directory model.
	if cs := Choices(Repo{ID: "a/b-GGUF", Files: []RepoFile{{Name: "config.json"}, {Name: "b.gguf"}}}); len(cs) != 1 || cs[0].Directory {
		t.Fatalf("%+v", cs)
	}
}

// addDirRepo serves an MLX-style model folder; it returns the folder's model bytes.
func (e *env) addDirRepo(repo, rev, config string) (shard1, shard2 []byte) {
	shard1, shard2 = blob(300<<10, 7), blob(200<<10, 8)
	e.hub.addRepo(repo, rev, map[string][]byte{
		"config.json":                      []byte(config),
		"model-00001-of-00002.safetensors": shard1,
		"model-00002-of-00002.safetensors": shard2,
		"model.safetensors.index.json":     []byte(`{"weight_map":{}}`),
		"tokenizer.json":                   []byte(`{}`),
		"tokenizer_config.json":            []byte(`{}`),
		"chat_template.jinja":              []byte(`{{ messages }}`),
		"images/card.png":                  []byte("png"),
	})
	return shard1, shard2
}

// A Hugging Face folder installs from the Models page's repo path: resumable over several
// slices, registered as one ready model with the prompt provider and fold flag inferred,
// run on MLX on a Mac (text-only, with a note), and deleted as a folder.
func TestInstallDirectoryModel(t *testing.T) {
	e := newEnv(t)
	e.mgr.Binaries.Platform = mac
	repo := "mlx-community/Qwen3.5-9B-4bit"
	s1, s2 := e.addDirRepo(repo, "r1", `{"model_type":"qwen3_5","architectures":["Qwen3_5ForConditionalGeneration"]}`)
	e.hub.cutOnce["/"+repo+"/resolve/r1/model-00001-of-00002.safetensors"] = true
	e.hub.throttle = 5 * time.Millisecond
	e.mgr.SliceDuration = 40 * time.Millisecond
	e.start()

	inst, _, err := e.mgr.Install(e.ctx, InstallRequest{Repo: repo, File: DirChoiceFile, Assign: []string{"live"}})
	if err != nil {
		t.Fatal(err)
	}
	if inst.ModelID != "qwen3.5-9b-4bit" || inst.EngineKind != "" || inst.MMProjID != "" {
		t.Fatalf("%+v", inst)
	}
	firstJob := inst.JobID
	done := e.waitInstall(inst.ID, InstallDone)
	if done.JobID == firstJob {
		t.Fatal("expected continuation jobs")
	}
	dir := filepath.Join(e.home, "models", "mlx-community--Qwen3.5-9B-4bit")
	m, _ := e.mgr.Store.Get(e.ctx, inst.ModelID)
	if m.Path != dir || m.State != StateReady || m.Kind != "llm" || m.PromptProvider != "Qwen3_5_9B_Compressed" || m.External ||
		len(m.Files) != 7 || m.Size < int64(len(s1)+len(s2)) || m.Display != "Qwen3.5-9B-4bit" {
		t.Fatalf("%+v", m)
	}
	if !IsModelDir(dir) || !fileExists(filepath.Join(dir, "chat_template.jinja")) || pathExists(filepath.Join(dir, "images")) {
		t.Fatal("folder not laid out for the engine")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "model-00001-of-00002.safetensors")); string(b) != string(s1) {
		t.Fatal("resumed shard corrupt")
	}
	if pathExists(filepath.Join(e.home, "models", ".partial", inst.ModelID)) {
		t.Fatal("partials left")
	}
	if e.set.String(e.ctx, "llm.live.model", settings.Scope{}) != inst.ModelID {
		t.Fatal("not assigned")
	}

	info, err := e.mgr.Store.LookupModel(e.ctx, inst.ModelID)
	if err != nil || info.Engine != engine.KindMLX || !info.FoldSystemMessages || info.Path != dir {
		t.Fatalf("%+v %v", info, err)
	}
	src := engine.SettingsSource{Settings: e.set, Models: e.mgr.Store, Platform: mac}
	c, err := src.Label(e.ctx, "live")
	if err != nil || c.Kind != engine.KindMLX || !c.FoldSystemMessages || c.Problem != "" || c.MMProjPath != "" ||
		!strings.Contains(c.Note, "text-only") {
		t.Fatalf("%+v %v", c, err)
	}
	// An MLX build stays on MLX whatever dir_engine says.
	e.set.Set(e.ctx, "llm.live.dir_engine", "transformers", settings.Scope{})
	if c, _ := src.Label(e.ctx, "live"); c.Kind != engine.KindMLX {
		t.Fatalf("dir_engine overrode MLX: %s", c.Kind)
	}

	// Installing it again is the ready model.
	again, _, err := e.mgr.Install(e.ctx, InstallRequest{Repo: repo, File: DirChoiceFile})
	if err != nil {
		t.Fatal(err)
	}
	if again.ModelID != inst.ModelID || again.BytesTotal != 0 {
		t.Fatalf("%+v", again)
	}
	e.waitInstall(again.ID, InstallDone)

	if err := e.mgr.Delete(e.ctx, inst.ModelID, true); err != nil {
		t.Fatal(err)
	}
	if pathExists(dir) || e.set.String(e.ctx, "llm.live.model", settings.Scope{}) != "" {
		t.Fatal("folder kept / label not cleared")
	}
}

func TestInstallDirectoryRefusals(t *testing.T) {
	e := newEnv(t)
	e.mgr.Binaries.Platform = mac
	e.addDirRepo("acme/Llama-Dir", "r1", `{"model_type":"llama"}`)
	e.addDirRepo("mlx-community/Qwen3-8B-4bit", "r2", `{"model_type":"qwen3"}`)
	status := func(err error) int {
		re, ok := err.(*RequestError)
		if !ok {
			t.Fatalf("not a request error: %v", err)
		}
		return re.Status
	}
	_, _, err := e.mgr.Install(e.ctx, InstallRequest{Repo: "acme/Llama-Dir", File: DirChoiceFile})
	if err == nil || status(err) != http.StatusUnprocessableEntity || !strings.Contains(err.Error(), "prompt provider") ||
		!strings.Contains(err.Error(), "Qwen3_8B_Compressed") {
		t.Fatalf("unknown family: %v", err)
	}
	if _, _, err := e.mgr.Install(e.ctx, InstallRequest{Repo: "acme/Llama-Dir", File: DirChoiceFile, PromptProvider: "Llama3"}); err == nil ||
		status(err) != http.StatusUnprocessableEntity {
		t.Fatalf("bad provider: %v", err)
	}
	if _, _, err := e.mgr.Install(e.ctx, InstallRequest{Repo: "acme/Llama-Dir", File: DirChoiceFile, MMProjFile: "tokenizer.json",
		PromptProvider: "Qwen3_8B_Compressed"}); err == nil {
		t.Fatal("projector for a folder")
	}
	inst, _, err := e.mgr.Install(e.ctx, InstallRequest{Repo: "acme/Llama-Dir", File: DirChoiceFile, PromptProvider: "Qwen3_8B_Compressed"})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := e.mgr.Store.Get(e.ctx, inst.ModelID); m.PromptProvider != "Qwen3_8B_Compressed" || m.ID != "llama-dir" {
		t.Fatalf("%+v", m)
	}
	// An MLX build needs Apple Silicon; any folder needs some engine.
	e.mgr.Binaries.Platform = linux
	if _, _, err := e.mgr.Install(e.ctx, InstallRequest{Repo: "mlx-community/Qwen3-8B-4bit", File: DirChoiceFile}); err == nil ||
		!strings.Contains(err.Error(), "Apple Silicon") {
		t.Fatalf("mlx on linux: %v", err)
	}
	e.mgr.Binaries.Platform = engine.Platform{OS: "linux", Arch: "arm64"}
	if _, _, err := e.mgr.Install(e.ctx, InstallRequest{Repo: "acme/Llama-Dir", File: DirChoiceFile, ID: "other",
		PromptProvider: "Qwen3_8B_Compressed"}); err == nil || !strings.Contains(err.Error(), "no engine") {
		t.Fatalf("no dir engine: %v", err)
	}
}

func writeModelDir(t *testing.T, name, config string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	os.MkdirAll(dir, 0o755)
	for f, b := range map[string]string{"config.json": config, "model.safetensors": "weights", "tokenizer.json": "{}", "README.md": "hi"} {
		os.WriteFile(filepath.Join(dir, f), []byte(b), 0o644)
	}
	return dir
}

func TestRegisterDirectory(t *testing.T) {
	e := newEnv(t)
	e.mgr.Binaries.Platform = mac
	dir := writeModelDir(t, "Qwen3-8B-4bit", `{"model_type":"qwen3"}`)
	m, err := e.mgr.Register(e.ctx, RegisterRequest{Path: dir})
	if err != nil || m.ID != "qwen3-8b-4bit" || m.Kind != "llm" || m.PromptProvider != "Qwen3_8B_Compressed" || !m.External ||
		len(m.Files) != 3 || m.Size != int64(len(`{"model_type":"qwen3"}`)+len("weights")+2) || m.State != StateReady {
		t.Fatalf("%+v %v", m, err)
	}
	if err := e.mgr.Assign(e.ctx, "live", m.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.Delete(e.ctx, m.ID, true); err != nil || !IsModelDir(dir) {
		t.Fatal("registered folder removed", err)
	}
	if _, err := e.mgr.Register(e.ctx, RegisterRequest{Path: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "not a Hugging Face model directory") {
		t.Fatalf("empty folder: %v", err)
	}
	llama := writeModelDir(t, "my-llama", `{"model_type":"llama"}`)
	if _, err := e.mgr.Register(e.ctx, RegisterRequest{Path: llama}); err == nil || !strings.Contains(err.Error(), "prompt provider") {
		t.Fatalf("unknown family: %v", err)
	}
	if m, err := e.mgr.Register(e.ctx, RegisterRequest{Path: llama, PromptProvider: "Qwen3_14B_Compressed"}); err != nil ||
		m.PromptProvider != "Qwen3_14B_Compressed" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := e.mgr.Register(e.ctx, RegisterRequest{Path: dir, Kind: "embedding", ID: "x"}); err == nil {
		t.Fatal("folder as an embedding model")
	}
}

// A label can't be saved with a folder cc couldn't prompt; a folder whose provider is known
// (inferred, or registered with one) saves and folds like its GGUF twin.
func TestLabelsDirectoryPromptProvider(t *testing.T) {
	e := newEnv(t)
	e.mgr.Binaries.Platform = mac
	llama := writeModelDir(t, "my-llama", `{"model_type":"llama"}`)
	err := e.mgr.UpdateLabels(e.ctx, map[string]map[string]any{"live": {"model": llama}})
	if err == nil || !strings.Contains(err.Error(), "prompt provider") {
		t.Fatalf("unknown family: %v", err)
	}
	if _, err := e.mgr.Register(e.ctx, RegisterRequest{Path: llama, PromptProvider: "Qwen3_8B_Compressed"}); err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.UpdateLabels(e.ctx, map[string]map[string]any{"live": {"model": llama}}); err != nil {
		t.Fatalf("registered with a provider: %v", err)
	}
	// An installed folder without one (made before this check) is refused by id too.
	e.mgr.Store.Upsert(e.ctx, Model{ID: "old", Kind: "llm", Display: "o", Path: llama, State: StateReady})
	if err := e.mgr.UpdateLabels(e.ctx, map[string]map[string]any{"live": {"model": "old"}}); err == nil {
		t.Fatal("folder without a provider")
	}

	q35 := writeModelDir(t, "Qwen3.5-9B-4bit", `{"model_type":"qwen3_5"}`)
	if err := e.mgr.UpdateLabels(e.ctx, map[string]map[string]any{"live": {"model": q35}}); err != nil {
		t.Fatal(err)
	}
	src := engine.SettingsSource{Settings: e.set, Models: e.mgr.Store, Platform: mac}
	if c, _ := src.Label(e.ctx, "live"); c.Kind != engine.KindMLX || !c.FoldSystemMessages || c.Problem != "" {
		t.Fatalf("path to a Qwen 3.5 folder: %+v", c)
	}
	// fold_system_messages=off still wins.
	e.set.Set(e.ctx, "llm.live.fold_system_messages", "off", settings.Scope{})
	if c, _ := src.Label(e.ctx, "live"); c.FoldSystemMessages {
		t.Fatal("fold off ignored")
	}
}

func dirCatalog(e *env) {
	e.addDirRepo("mlx-community/Tiny-4bit", "r3", `{"model_type":"qwen3_5"}`)
	cat := append(Catalog(), Entry{ID: "tiny-mlx", Display: "Tiny MLX", Kind: "llm", Repo: "mlx-community/Tiny-4bit",
		File: DirChoiceFile, Size: 500 << 10, Directory: true, ContextDefault: 4096, KVBytesPerTok: 1024,
		PromptProvider: "Qwen3_5_9B_Compressed", FoldSystemMessages: true})
	b, _ := json.Marshal(cat)
	old := catalogData
	catalogData = b
	e.t.Cleanup(func() { catalogData = old })
}

func TestInstallCatalogDirectory(t *testing.T) {
	e := newEnv(t)
	e.mgr.Binaries.Platform = mac
	dirCatalog(e)
	e.start()
	inst, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "tiny-mlx", Assign: []string{"background"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitInstall(inst.ID, InstallDone)
	m, _ := e.mgr.Store.Get(e.ctx, "tiny-mlx")
	if m.CatalogID != "tiny-mlx" || m.Revision != "r3" || m.PromptProvider != "Qwen3_5_9B_Compressed" || m.ContextDefault != 4096 ||
		!IsModelDir(m.Path) || m.Display != "Tiny MLX" {
		t.Fatalf("%+v", m)
	}
	if info, err := e.mgr.Store.LookupModel(e.ctx, "tiny-mlx"); err != nil || !info.FoldSystemMessages || info.Engine != engine.KindMLX {
		t.Fatalf("%+v %v", info, err)
	}
	e.mgr.Binaries.Platform = linux
	if _, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "tiny-mlx"}); err == nil || !strings.Contains(err.Error(), "Apple Silicon") {
		t.Fatalf("mlx entry on linux: %v", err)
	}
}

func macHW(gb int64) engine.Hardware {
	usable := gb << 10 * 7 / 10
	return engine.Hardware{Flavour: engine.FlavourMetal, RAMMB: gb << 10, Devices: []engine.Device{
		{Backend: engine.FlavourMetal, ID: "MTL0", Name: "Apple M3", TotalMB: usable, FreeMB: usable}}}
}

// On a Mac the MLX build is recommended, sized by memory: the 9B for 16 GB (the 27B and 35B
// need 32 GB), a bigger one with more. Elsewhere the MLX entries are never recommended.
func TestRecommendMLXByMemory(t *testing.T) {
	if r := Recommend(macHW(16), nil, nil); r["live"] != "qwen3.5-9b-mlx" || r["background"] != "qwen3.5-9b-mlx" {
		t.Fatalf("16 GB Mac: %v", r)
	}
	e27, _ := CatalogEntry("qwen3.5-27b-mlx")
	e35, _ := CatalogEntry("qwen3.5-35b-a3b-mlx")
	if EntryFit(macHW(16), e27).Verdict != "too_big" || EntryFit(macHW(16), e35).Verdict != "too_big" {
		t.Fatal("27B/35B MLX judged runnable on 16 GB")
	}
	if e27.MinMemoryMB != 32<<10 || e35.MinMemoryMB != 32<<10 {
		t.Fatal("27B/35B MLX min memory")
	}
	if r := Recommend(macHW(32), nil, nil); r["live"] != "qwen3.5-27b-mlx" {
		t.Fatalf("32 GB Mac: %v", r)
	}
	// 8 GB: no MLX entry fits; the smallest GGUF, as before.
	if r := Recommend(macHW(8), nil, nil); r["live"] != "qwen3-4b" {
		t.Fatalf("8 GB Mac: %v", r)
	}
	cuda := engine.Hardware{Flavour: engine.FlavourCUDA, Devices: []engine.Device{
		{Backend: engine.FlavourCUDA, Index: 0, Name: "RTX 4090", TotalMB: 49152, FreeMB: 49000}}}
	if r := Recommend(cuda, nil, nil); strings.HasSuffix(r["live"], "-mlx") {
		t.Fatalf("MLX recommended on CUDA: %v", r)
	}
}

// The Models page's Browse lists a repo's folder as one choice, with the engine that runs it
// here (or why none can).
func TestHFRepoAPIDirectoryChoice(t *testing.T) {
	e := newEnv(t)
	e.mgr.Binaries.Platform = mac
	e.addDirRepo("mlx-community/Qwen3.5-9B-4bit", "r1", `{"model_type":"qwen3_5"}`)
	mux := http.NewServeMux()
	(&API{Manager: e.mgr, Resolver: e.labels}).Mount(mux, func(http.ResponseWriter, *http.Request) bool { return true })
	get := func() []hfChoice {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/hf/mlx-community/Qwen3.5-9B-4bit", nil))
		var body struct {
			Files []hfChoice `json:"files"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return body.Files
	}
	cs := get()
	if len(cs) != 1 || !cs[0].Directory || cs[0].Engine != "mlx-lm" || cs[0].Unavailable != "" || cs[0].PromptProvider != "Qwen3_5_9B_Compressed" ||
		cs[0].Fit.Verdict == "" {
		t.Fatalf("%+v", cs)
	}
	e.mgr.Binaries.Platform = linux
	if cs := get(); cs[0].Engine != "" || !strings.Contains(cs[0].Unavailable, "Apple Silicon") {
		t.Fatalf("linux: %+v", cs)
	}
}

// A folder whose name doesn't say its family gets its provider from the repo's config.json,
// and Browse lists the providers to pick from when nothing says.
func TestHFRepoAPIDirectoryProviderFromConfig(t *testing.T) {
	e := newEnv(t)
	e.mgr.Binaries.Platform = mac
	e.addDirRepo("acme/my-finetune-mlx", "r1", `{"model_type":"qwen3","hidden_size":4096,"num_hidden_layers":36,"intermediate_size":12288,"vocab_size":151936}`)
	e.addDirRepo("acme/llama-mlx", "r2", `{"model_type":"llama"}`)
	mux := http.NewServeMux()
	(&API{Manager: e.mgr, Resolver: e.labels}).Mount(mux, func(http.ResponseWriter, *http.Request) bool { return true })
	get := func(repo string) (cs []hfChoice, providers []string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/hf/"+repo, nil))
		var body struct {
			Files     []hfChoice `json:"files"`
			Providers []string   `json:"prompt_providers"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return body.Files, body.Providers
	}
	if cs, _ := get("acme/my-finetune-mlx"); len(cs) != 1 || cs[0].PromptProvider != "Qwen3_8B_Compressed" {
		t.Fatalf("%+v", cs)
	}
	if cs, ps := get("acme/llama-mlx"); len(cs) != 1 || cs[0].PromptProvider != "" || !slices.Equal(ps, KeptPromptProviders) {
		t.Fatalf("%+v %v", cs, ps)
	}
}

// The installed list says which models are folders (the Models page marks them).
func TestInstalledAPIDirectoryFlag(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(t.TempDir(), "Qwen3.5-9B-4bit")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"model_type":"qwen3_5"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "model.safetensors"), []byte("w"), 0o644)
	gguf := filepath.Join(t.TempDir(), "m.gguf")
	os.WriteFile(gguf, []byte("g"), 0o644)
	e.mgr.Store.Upsert(e.ctx, Model{ID: "folder", Kind: "llm", Display: "f", Path: dir, State: StateReady, External: true})
	e.mgr.Store.Upsert(e.ctx, Model{ID: "file", Kind: "llm", Display: "g", Path: gguf, State: StateReady, External: true})
	mux := http.NewServeMux()
	(&API{Manager: e.mgr, Resolver: e.labels}).Mount(mux, func(http.ResponseWriter, *http.Request) bool { return true })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/installed", nil))
	var body struct {
		Models []struct {
			ID        string `json:"id"`
			Directory bool   `json:"directory"`
		} `json:"models"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := map[string]bool{}
	for _, m := range body.Models {
		got[m.ID] = m.Directory
	}
	if !got["folder"] || got["file"] {
		t.Fatalf("%+v", body.Models)
	}
}

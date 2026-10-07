package models

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// In-binary model kinds: run by sherpa-onnx inside jarvisd (PLAN §3.3), so they get no engine
// process and no Resolve endpoint. The voice module asks ModelPath for them.
const (
	KindTTS     = "tts"     // Kokoro: a directory (model.onnx, voices.bin, tokens.txt, lexicons, espeak-ng-data)
	KindSpeaker = "speaker" // speaker embedding .onnx (3D-Speaker ERes2Net)
)

// VoiceLabel is a label for an in-binary model: the setting naming the model in use.
type VoiceLabel struct {
	Name         string
	Key          string // settings key holding an installed id or an absolute path
	ModelKind    string
	DefaultModel string // used when the setting is empty and this model is installed
}

// VoiceLabels are the in-binary labels.
var VoiceLabels = []VoiceLabel{
	{Name: "tts", Key: "tts.model", ModelKind: KindTTS, DefaultModel: "kokoro-multi-lang-v1_0"},
	{Name: "speaker", Key: "speaker.model", ModelKind: KindSpeaker, DefaultModel: "eres2net-voxceleb-16k"},
}

// SettingDefinitions is every setting of the engine stack and the model manager: the llm
// module passes these (plus its own) to settings.New.
func SettingDefinitions() []settings.Definition {
	defs := engine.SettingDefinitions()
	for _, v := range VoiceLabels {
		defs = append(defs, settings.Definition{
			Key: v.Key, Category: "engine." + v.Name, Type: settings.String, Default: "", RequiresReload: true,
			Description: "Installed " + v.ModelKind + " model id, or an absolute path; empty = " + v.DefaultModel + " when installed",
		})
	}
	return defs
}

// labelRef is any label the manager can assign: an engine label or a voice label.
type labelRef struct {
	Name      string
	Prefix    string // settings prefix; the model key is Prefix + ".model"
	ModelKind string
	Voice     bool
}

func allLabels() []labelRef {
	var out []labelRef
	for _, d := range engine.LabelDefs {
		out = append(out, labelRef{Name: d.Name, Prefix: d.Prefix, ModelKind: d.ModelKind})
	}
	for _, v := range VoiceLabels {
		out = append(out, labelRef{Name: v.Name, Prefix: strings.TrimSuffix(v.Key, ".model"), ModelKind: v.ModelKind, Voice: true})
	}
	return out
}

func lookupLabel(name string) (labelRef, bool) {
	for _, l := range allLabels() {
		if l.Name == name {
			return l, true
		}
	}
	return labelRef{}, false
}

// VoiceModel is what the voice module needs to load an in-binary model.
type VoiceModel struct {
	Label string `json:"label"`
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	// Path is the .onnx file (speaker) or the model directory (tts).
	Path    string `json:"path"`
	Problem string `json:"problem,omitempty"`
}

// ModelPath resolves a voice label ("tts" or "speaker") to the model to load: the configured
// id or path, else the default model if installed, else any ready model of that kind.
func (m *Manager) ModelPath(ctx context.Context, label string) (VoiceModel, bool) {
	var v VoiceLabel
	for _, x := range VoiceLabels {
		if x.Name == label {
			v = x
		}
	}
	if v.Name == "" {
		return VoiceModel{Label: label, Problem: "unknown voice label"}, false
	}
	out := VoiceModel{Label: label, Kind: v.ModelKind}
	ref := strings.TrimSpace(m.Settings.String(ctx, v.Key, settings.Scope{}))
	if filepath.IsAbs(ref) {
		if _, err := os.Stat(ref); err != nil {
			out.Problem = err.Error()
			return out, false
		}
		out.ID, out.Path = ref, ref
		return out, true
	}
	try := func(id string) bool {
		info, err := m.Store.LookupModel(ctx, id)
		if err != nil || info.Kind != v.ModelKind {
			return false
		}
		out.ID, out.Path = info.ID, info.Path
		return true
	}
	if ref != "" {
		if try(ref) {
			return out, true
		}
		out.Problem = "model " + ref + " is not installed"
		return out, false
	}
	if try(v.DefaultModel) {
		return out, true
	}
	list, _ := m.Store.List(ctx)
	for _, mod := range list {
		if mod.Kind == v.ModelKind && try(mod.ID) {
			return out, true
		}
	}
	return out, false
}

// VoiceStatus reports every voice label, for the labels API and doctor.
func (m *Manager) VoiceStatus(ctx context.Context) []VoiceModel {
	var out []VoiceModel
	for _, v := range VoiceLabels {
		vm, ok := m.ModelPath(ctx, v.Name)
		if !ok && vm.Problem == "" {
			vm.Problem = "not configured"
		}
		out = append(out, vm)
	}
	return out
}

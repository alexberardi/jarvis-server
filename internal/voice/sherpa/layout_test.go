package sherpa

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// mirrored lists every Go mirror struct with its C typedef name and C field names in declaration
// order. The test compiles a C program against the vendored header and checks sizeof + every
// offsetof against the Go layout, so a sherpa-onnx upgrade that changes the ABI fails loudly.
var mirrored = []struct {
	goType reflect.Type
	cName  string
	fields []string
}{
	{reflect.TypeOf(speakerEmbeddingExtractorConfig{}), "SherpaOnnxSpeakerEmbeddingExtractorConfig",
		[]string{"model", "num_threads", "debug", "provider"}},
	{reflect.TypeOf(ttsVitsModelConfig{}), "SherpaOnnxOfflineTtsVitsModelConfig",
		[]string{"model", "lexicon", "tokens", "data_dir", "noise_scale", "noise_scale_w", "length_scale", "dict_dir"}},
	{reflect.TypeOf(ttsMatchaModelConfig{}), "SherpaOnnxOfflineTtsMatchaModelConfig",
		[]string{"acoustic_model", "vocoder", "lexicon", "tokens", "data_dir", "noise_scale", "length_scale", "dict_dir"}},
	{reflect.TypeOf(ttsKokoroModelConfig{}), "SherpaOnnxOfflineTtsKokoroModelConfig",
		[]string{"model", "voices", "tokens", "data_dir", "length_scale", "dict_dir", "lexicon", "lang"}},
	{reflect.TypeOf(ttsKittenModelConfig{}), "SherpaOnnxOfflineTtsKittenModelConfig",
		[]string{"model", "voices", "tokens", "data_dir", "length_scale"}},
	{reflect.TypeOf(ttsZipvoiceModelConfig{}), "SherpaOnnxOfflineTtsZipvoiceModelConfig",
		[]string{"tokens", "encoder", "decoder", "vocoder", "data_dir", "lexicon", "feat_scale", "t_shift", "target_rms", "guidance_scale"}},
	{reflect.TypeOf(ttsPocketModelConfig{}), "SherpaOnnxOfflineTtsPocketModelConfig",
		[]string{"lm_flow", "lm_main", "encoder", "decoder", "text_conditioner", "vocab_json", "token_scores_json", "voice_embedding_cache_capacity"}},
	{reflect.TypeOf(ttsSupertonicModelConfig{}), "SherpaOnnxOfflineTtsSupertonicModelConfig",
		[]string{"duration_predictor", "text_encoder", "vector_estimator", "vocoder", "tts_json", "unicode_indexer", "voice_style"}},
	{reflect.TypeOf(ttsModelConfig{}), "SherpaOnnxOfflineTtsModelConfig",
		[]string{"vits", "num_threads", "debug", "provider", "matcha", "kokoro", "kitten", "zipvoice", "pocket", "supertonic"}},
	{reflect.TypeOf(ttsConfig{}), "SherpaOnnxOfflineTtsConfig",
		[]string{"model", "rule_fsts", "max_num_sentences", "rule_fars", "silence_scale"}},
	{reflect.TypeOf(generationConfig{}), "SherpaOnnxGenerationConfig",
		[]string{"silence_scale", "speed", "sid", "reference_audio", "reference_audio_len", "reference_sample_rate", "reference_text", "num_steps", "extra"}},
	{reflect.TypeOf(generatedAudio{}), "SherpaOnnxGeneratedAudio",
		[]string{"samples", "n", "sample_rate"}},
}

func TestStructLayout(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler; layout is checked in CI on linux and darwin")
	}
	header, err := filepath.Abs("../../../third_party/sherpa/include/c-api.h")
	if err != nil {
		t.Fatal(err)
	}

	var src strings.Builder
	fmt.Fprintf(&src, "#include <stdio.h>\n#include <stddef.h>\n#include %q\nint main(void){\n", header)
	for _, m := range mirrored {
		if m.goType.NumField() != len(m.fields) {
			t.Fatalf("%s: Go struct has %d fields, C field list has %d", m.cName, m.goType.NumField(), len(m.fields))
		}
		fmt.Fprintf(&src, "printf(\"%s size %%zu\\n\", sizeof(%s));\n", m.cName, m.cName)
		for _, f := range m.fields {
			fmt.Fprintf(&src, "printf(\"%s.%s %%zu\\n\", offsetof(%s, %s));\n", m.cName, f, m.cName, f)
		}
	}
	src.WriteString("return 0;}\n")

	dir := t.TempDir()
	cFile, bin := filepath.Join(dir, "layout.c"), filepath.Join(dir, "layout")
	if err := os.WriteFile(cFile, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(cc, "-o", bin, cFile).CombinedOutput(); err != nil {
		t.Fatalf("compile layout probe: %v\n%s", err, out)
	}
	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]uintptr{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var k string
		var v uintptr
		if _, err := fmt.Sscanf(strings.Replace(line, " size ", ".#size ", 1), "%s %d", &k, &v); err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		got[k] = v
	}
	for _, m := range mirrored {
		if want := got[m.cName+".#size"]; m.goType.Size() != want {
			t.Errorf("%s: sizeof C=%d Go=%d (%s/%s)", m.cName, want, m.goType.Size(), runtime.GOOS, runtime.GOARCH)
		}
		for i, f := range m.fields {
			if want, goOff := got[m.cName+"."+f], m.goType.Field(i).Offset; goOff != want {
				t.Errorf("%s.%s: offset C=%d Go=%d (Go field %s)", m.cName, f, want, goOff, m.goType.Field(i).Name)
			}
		}
	}
}

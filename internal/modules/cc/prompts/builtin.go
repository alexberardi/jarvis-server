package prompts

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// builtinFS holds the shipped prompts, one text file per name (builtin/<name>.txt).
//
//go:embed builtin/*.txt
var builtinFS embed.FS

func init() {
	for _, n := range BuiltinNames() {
		text, _ := Builtin(n)
		if err := Validate(text); err != nil {
			panic("prompts: built-in " + n + ": " + err.Error())
		}
	}
}

// renamed are built-ins' old names, still accepted (stored settings, the legacy goldens).
var renamed = map[string]string{"ChatGPTOpenAI": Cloud}

// Builtin is the text of the built-in prompt name, or false when there is none. Only the
// exact name: a path form of one ("./x", "a/../x") is not a name.
func Builtin(name string) (string, bool) {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return "", false
	}
	if n, ok := renamed[name]; ok {
		name = n
	}
	b, err := builtinFS.ReadFile(path.Join("builtin", name+".txt"))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// builtinPrefs are the built-ins with LLM values of their own. Gemma 4's template has
// enable_thinking, which the template check reads as Qwen's /think suffix; Gemma takes the
// switch through chat_template_kwargs only, so its prompt says none.
var builtinPrefs = map[string]ModePrefs{Gemma4: {ToolCalling: string(ToolsNative), ThinkingSwitch: "none"}}

// BuiltinPrefs are the built-in prompt name's own LLM values (zero for most: the model's
// template decides).
func BuiltinPrefs(name string) ModePrefs { return builtinPrefs[name] }

// Or fills p's unset values from q.
func (p ModePrefs) Or(q ModePrefs) ModePrefs {
	if p.ToolCalling == "" {
		p.ToolCalling = q.ToolCalling
	}
	if p.ThinkingSwitch == "" {
		p.ThinkingSwitch = q.ThinkingSwitch
	}
	return p
}

// BuiltinNames lists the built-in prompts, sorted.
func BuiltinNames() []string {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		panic(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".txt"))
	}
	sort.Strings(names)
	return names
}

// CheckBuiltin says whether name is a built-in prompt. There is no default and no remap of
// dropped legacy names (D11): anything else is an error.
func CheckBuiltin(name string) error {
	if _, ok := Builtin(name); !ok {
		return fmt.Errorf("unknown prompt provider %q (want one of %s)", name, strings.Join(BuiltinNames(), ", "))
	}
	return nil
}

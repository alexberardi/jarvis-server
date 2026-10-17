package prompts

import (
	"embed"
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

// Builtin is the text of the built-in prompt name, or false when there is none.
func Builtin(name string) (string, bool) {
	b, err := builtinFS.ReadFile(path.Join("builtin", name+".txt"))
	if err != nil {
		return "", false
	}
	return string(b), true
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

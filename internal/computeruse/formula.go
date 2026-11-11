package computeruse

import (
	"fmt"
	"slices"
	"strings"
)

// Spreadsheet formulas a model writes (write_document, computer_files doc_edit) are checked
// before LibreOffice sees them: a formula can read another file ('file:///…'#$Sheet.A1) and send
// what it read over the network (WEBSERVICE), and the model writes from untrusted pages, emails
// and attachments (PR #547 review C1). So only these functions, and no ' or # outside a string.
// jarvisd checks so the model can be asked again; the agent checks again before office.py runs.

// formulaFunctions are the functions a formula may call (lower case, as set stores them).
var formulaFunctions = set(
	"SUM", "SUMIF", "SUMIFS", "SUMPRODUCT", "PRODUCT",
	"AVERAGE", "AVERAGEIF", "AVERAGEIFS", "MEDIAN", "COUNT", "COUNTA", "COUNTBLANK", "COUNTIF", "COUNTIFS",
	"MIN", "MAX", "ROUND", "ROUNDUP", "ROUNDDOWN", "INT", "MOD", "ABS",
	"IF", "IFERROR", "AND", "OR", "NOT",
	"VLOOKUP", "HLOOKUP", "INDEX", "MATCH",
	"CONCATENATE", "TEXT", "LEN", "UPPER", "LOWER", "TRIM",
	"TODAY", "DATE", "YEAR", "MONTH", "DAY",
)

// maxFormula bounds a formula's length.
const maxFormula = 1000

// FormulaProblem is why v, a cell's value, may not be written: "" when it may, or isn't a
// formula (no leading "=").
func FormulaProblem(v string) string {
	if !strings.HasPrefix(v, "=") {
		return ""
	}
	if len(v) > maxFormula {
		return fmt.Sprintf("a formula is longer than %d characters", maxFormula)
	}
	for i := 1; i < len(v); i++ {
		switch c := v[i]; {
		case c == '"':
			end := stringEnd(v, i)
			if end < 0 {
				return v + ": a formula has an unclosed string"
			}
			i = end
		case c == '\'' || c == '#':
			return fmt.Sprintf("%s: a formula may refer only to cells of this file (no %c)", v, c)
		case isNameStart(c):
			j := i
			for j < len(v) && isNameChar(v[j]) {
				j++
			}
			k := j
			for k < len(v) && v[k] == ' ' {
				k++
			}
			if name := v[i:j]; k < len(v) && v[k] == '(' && !formulaFunctions[strings.ToLower(name)] {
				return fmt.Sprintf("%s: a formula may use only %s, not %s", v, FormulaFunctionList(), strings.ToUpper(name))
			}
			i = j - 1
		}
	}
	return ""
}

// stringEnd is the index of the quote closing the string literal opening at v[start] (a doubled
// quote inside it is a quote), or -1.
func stringEnd(v string, start int) int {
	for i := start + 1; i < len(v); i++ {
		if v[i] != '"' {
			continue
		}
		if i+1 < len(v) && v[i+1] == '"' {
			i++
			continue
		}
		return i
	}
	return -1
}

// FormulaFunctionList is the allowed functions for a message or a prompt, sorted.
func FormulaFunctionList() string {
	names := make([]string, 0, len(formulaFunctions))
	for n := range formulaFunctions {
		names = append(names, strings.ToUpper(n))
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

func isNameStart(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_'
}

func isNameChar(c byte) bool {
	return isNameStart(c) || c >= '0' && c <= '9' || c == '.'
}

// EditsFormulaProblem is FormulaProblem for the first bad cell value in spreadsheet edits
// (office.py's ops: "value" of set, "values" of append_row), or "".
func EditsFormulaProblem(ops []any) string {
	for _, o := range ops {
		om, _ := o.(map[string]any)
		vals, _ := om["values"].([]any)
		for _, v := range append([]any{om["value"]}, vals...) {
			if s, ok := v.(string); ok {
				if p := FormulaProblem(s); p != "" {
					return p
				}
			}
		}
	}
	return ""
}

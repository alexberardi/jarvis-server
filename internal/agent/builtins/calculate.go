package builtins

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// Calculate is calculate (commands/calculator_command.py): arithmetic on two numbers, and its
// fast paths ("what's 12 times 8", "add 5 and 3") with no model.
type Calculate struct {
	Store *nodesettings.Store // the fast-path switches; nil: all on
}

const calculateName = "calculate"

func (c *Calculate) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{def(calculateName)}
}

func (c *Calculate) Call(_ context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	if name != calculateName {
		return nil, false
	}
	if out := validated(def(name), args); out != nil {
		return out, true
	}
	return calculate(args), true
}

func (c *Calculate) Direct(ctx context.Context, said string) (map[string]any, bool) {
	return directBy(ctx, c.Store, calculateName, calculatePatterns, said, func(a map[string]any) map[string]any {
		out, _ := c.Call(ctx, calculateName, a, node.Request{})
		return out
	})
}

func (c *Calculate) FastPaths(command string) []nodesettings.FastPath {
	if command != calculateName {
		return nil
	}
	return patternList(calculatePatterns)
}

var opText = map[string]string{"add": "plus", "subtract": "minus", "multiply": "times", "divide": "divided by"}

// calculate is CalculatorCommand.run (the operation is one of the enum's, validated already).
func calculate(args map[string]any) map[string]any {
	n1, n2 := pyFloat(args["num1"]), pyFloat(args["num2"])
	op, _ := args["operation"].(string)
	var result float64
	switch op {
	case "add":
		result = n1 + n2
	case "subtract":
		result = n1 - n2
	case "multiply":
		result = n1 * n2
	case "divide":
		if n2 == 0 {
			return failure("Division by zero", map[string]any{"num1": n1, "num2": n2, "operation": op})
		}
		result = n1 / n2
	}
	text := opText[op]
	resultText := pyRepr(result)
	spoken := spokenNumber(result)
	if op == "divide" && result != math.Trunc(result) {
		spoken = fmt.Sprintf("%.2f", result)
	}
	return finish(map[string]any{
		"result": result, "calculation": pyRepr(n1) + " " + text + " " + pyRepr(n2) + " = " + resultText,
		"operation": op, "num1": n1, "num2": n2, "operation_text": text,
		"message": spokenNumber(n1) + " " + text + " " + spokenNumber(n2) + " equals " + spoken + ".",
	}, true, true) // follow_up_response
}

// pyFloat is Python's float() of a validated JSON number (a bool is 1 or 0).
func pyFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case int:
		return float64(x)
	}
	f, _ := strconv.ParseFloat(fmt.Sprint(v), 64)
	return f
}

// pyRepr is Python's str() of a float: the shortest round-trip digits, fixed between 1e-4 and
// 1e16 (with ".0" on a whole number), scientific outside.
func pyRepr(x float64) string {
	switch {
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	case math.IsNaN(x):
		return "nan"
	}
	if a := math.Abs(x); a != 0 && (a >= 1e16 || a < 1e-4) {
		return strconv.FormatFloat(x, 'e', -1, 64)
	}
	s := strconv.FormatFloat(x, 'f', -1, 64)
	if !strings.ContainsAny(s, ".") {
		s += ".0"
	}
	return s
}

// spokenNumber is the command's _fmt: a whole number without decimals, else Python's "{:g}".
func spokenNumber(x float64) string {
	if x == math.Trunc(x) && !math.IsInf(x, 0) {
		return strconv.FormatFloat(x, 'f', 0, 64)
	}
	return fmt.Sprintf("%.6g", x)
}

var (
	arithmeticRe = regexp.MustCompile(`(?i)^(?:what(?:'?s|\s+is)?\s+)?(?P<n1>-?\d+(?:\.\d+)?)\s+(?P<op>plus|\+|minus|\-|times|x|multiplied\s+by|\*|divided\s+by|over|/)\s+(?P<n2>-?\d+(?:\.\d+)?)\s*[?.!]*$`)
	imperativeRe = regexp.MustCompile(`(?i)^(?P<verb>add|subtract|multiply|divide)\s+(?P<n1>-?\d+(?:\.\d+)?)\s+(?:and|by|from)\s+(?P<n2>-?\d+(?:\.\d+)?)\s*[?.!]*$`)
	spacesRe     = regexp.MustCompile(`\s+`)
)

var opWords = map[string]string{
	"plus": "add", "+": "add", "minus": "subtract", "-": "subtract",
	"times": "multiply", "x": "multiply", "multiplied by": "multiply", "*": "multiply",
	"divided by": "divide", "over": "divide", "/": "divide",
}

func group(re *regexp.Regexp, m []string, name string) string { return m[re.SubexpIndex(name)] }

func parseNum(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

var calculatePatterns = []pattern{
	regexPattern("calculate.infix", "Bypass LLM for 'N plus/minus/times/divided by M' phrasings", "what's 5 plus 3",
		arithmeticRe, func(m []string) (map[string]any, bool) {
			op, ok := opWords[spacesRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(group(arithmeticRe, m, "op"))), " ")]
			if !ok {
				return nil, false
			}
			return map[string]any{"num1": parseNum(group(arithmeticRe, m, "n1")), "num2": parseNum(group(arithmeticRe, m, "n2")),
				"operation": op}, true
		}),
	regexPattern("calculate.imperative", "Bypass LLM for 'add N and M' / 'divide N by M' / 'multiply N by M' / 'subtract N from M'",
		"add 5 and 3", imperativeRe, func(m []string) (map[string]any, bool) {
			verb := strings.ToLower(group(imperativeRe, m, "verb"))
			n1, n2 := parseNum(group(imperativeRe, m, "n1")), parseNum(group(imperativeRe, m, "n2"))
			if verb == "subtract" { // "subtract 7 from 22" is 22 - 7
				n1, n2 = n2, n1
			}
			return map[string]any{"num1": n1, "num2": n2, "operation": verb}, true
		}),
}

// CalculateMatch is the arguments Direct would calculate with (for comparing with N:'s pre_route).
func CalculateMatch(said string) (Match, bool) {
	for _, p := range calculatePatterns {
		if args, ok := p.match(said); ok {
			return Match{calculateName, args}, true
		}
	}
	return Match{}, false
}

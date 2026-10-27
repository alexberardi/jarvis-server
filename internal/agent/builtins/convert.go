package builtins

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// Convert is convert_measurement (commands/measurement_conversion_command.py): distance,
// volume, weight and temperature, and its fast paths ("how many cups in a gallon").
type Convert struct {
	Store *nodesettings.Store // the fast-path switches; nil: all on
}

const convertName = "convert_measurement"

func (c *Convert) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{def(convertName)}
}

func (c *Convert) Call(_ context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	if name != convertName {
		return nil, false
	}
	if out := validated(def(name), args); out != nil {
		return out, true
	}
	return convert(args), true
}

func (c *Convert) Direct(ctx context.Context, said string) (map[string]any, bool) {
	return directBy(ctx, c.Store, convertName, convertPatterns, said, func(a map[string]any) map[string]any {
		out, _ := c.Call(ctx, convertName, a, node.Request{})
		return out
	})
}

func (c *Convert) FastPaths(command string) []nodesettings.FastPath {
	if command != convertName {
		return nil
	}
	return patternList(convertPatterns)
}

// ConvertMatch is the arguments Direct would convert with (for comparing with N:'s pre_route).
func ConvertMatch(said string) (Match, bool) {
	for _, p := range convertPatterns {
		if args, ok := p.match(said); ok {
			return Match{convertName, args}, true
		}
	}
	return Match{}, false
}

// baseUnits are BASE_CONVERSIONS in their order (the error's supported_units): the factor to
// the category's base unit; temperatures are converted by formula.
var baseUnits = []struct {
	name   string
	factor float64
}{
	{"meters", 1}, {"kilometers", 1000}, {"centimeters", 0.01}, {"millimeters", 0.001}, {"miles", 1609.34},
	{"yards", 0.9144}, {"feet", 0.3048}, {"inches", 0.0254}, {"leagues", 4828.03},
	{"liters", 1}, {"milliliters", 0.001}, {"gallons", 3.78541}, {"quarts", 0.946353}, {"pints", 0.473176},
	{"cups", 0.236588}, {"tablespoons", 0.0147868}, {"teaspoons", 0.00492892}, {"fluid_ounces", 0.0295735},
	{"grams", 1}, {"kilograms", 1000}, {"milligrams", 0.001}, {"pounds", 453.592}, {"ounces", 28.3495},
	{"tons", 907185}, {"metric_tons", 1000000},
	{"celsius", 0}, {"fahrenheit", 0}, {"kelvin", 0},
}

var (
	distanceUnits = []string{"meters", "kilometers", "centimeters", "millimeters", "miles", "yards", "feet", "inches", "leagues"}
	volumeUnits   = []string{"liters", "milliliters", "gallons", "quarts", "pints", "cups", "tablespoons", "teaspoons", "fluid_ounces"}
	weightUnits   = []string{"grams", "kilograms", "milligrams", "pounds", "ounces", "tons", "metric_tons"}
	tempUnits     = []string{"celsius", "fahrenheit", "kelvin"}
)

func unitFactor(u string) (float64, bool) {
	for _, b := range baseUnits {
		if b.name == u {
			return b.factor, true
		}
	}
	return 0, false
}

func supportedUnits() []any {
	out := make([]any, len(baseUnits))
	for i, b := range baseUnits {
		out[i] = b.name
	}
	return out
}

// convert is MeasurementConversionCommand.run.
func convert(args map[string]any) map[string]any {
	value := 1.0
	if v, ok := args["value"]; ok {
		if v == nil {
			return paramError("float() argument must be a string or a real number, not 'NoneType'", args)
		}
		value = pyFloat(v)
	}
	from, _ := args["from_unit"].(string)
	to, _ := args["to_unit"].(string)
	from, to = strings.ToLower(from), strings.ToLower(to)
	category := ""
	if v, ok := args["category"]; ok {
		if v == nil {
			return failure("Unexpected error: 'NoneType' object has no attribute 'lower'",
				map[string]any{"parameters_received": args})
		}
		category, _ = v.(string)
		category = strings.ToLower(category)
	}
	if from == "" || to == "" {
		return failure("Missing from_unit or to_unit", map[string]any{"value": value, "from_unit": from, "to_unit": to, "category": category})
	}
	ff, okFrom := unitFactor(from)
	if !okFrom {
		return failure("Unsupported from_unit: "+from, map[string]any{"value": value, "from_unit": from, "to_unit": to, "supported_units": supportedUnits()})
	}
	tf, okTo := unitFactor(to)
	if !okTo {
		return failure("Unsupported to_unit: "+to, map[string]any{"value": value, "from_unit": from, "to_unit": to, "supported_units": supportedUnits()})
	}
	if slices.Contains(tempUnits, from) && slices.Contains(tempUnits, to) {
		result := convertTemperature(value, from, to)
		suffix := map[string]string{"celsius": "°C", "fahrenheit": "°F", "kelvin": "K"}[to]
		text := fmt.Sprintf("%.1f", result) + suffix
		return finish(map[string]any{"value": value, "from_unit": from, "to_unit": to, "result": result, "result_text": text,
			"conversion_type": "temperature", "message": pyRepr(value) + " " + from + " equals " + text + "."}, true, true)
	}
	var result float64
	switch {
	case from == to:
		result = value
	case sameCategory(from, to):
		result = value * ff / tf
	default:
		e := "Cannot convert between " + from + " and " + to + " - they are different measurement categories"
		return failure("Conversion error: "+e, map[string]any{"value": value, "from_unit": from, "to_unit": to})
	}
	var text string
	switch a := math.Abs(result); {
	case result == math.Trunc(result):
		text = fmt.Sprintf("%.0f", result)
	case a < 0.01:
		text = fmt.Sprintf("%.6f", result)
	case a < 0.1:
		text = fmt.Sprintf("%.4f", result)
	case a < 1:
		text = fmt.Sprintf("%.3f", result)
	default:
		text = fmt.Sprintf("%.2f", result)
	}
	msg := pyRepr(value) + " " + from + " equals " + text + " " + to
	if value == 1 {
		msg = "There are " + text + " " + to + " in 1 " + from
	}
	return finish(map[string]any{"value": value, "from_unit": from, "to_unit": to, "result": result, "result_text": text,
		"conversion_type": "standard", "message": msg + "."}, true, true)
}

// paramError is the command's except (ValueError, TypeError) branch.
func paramError(e string, args map[string]any) map[string]any {
	return failure("Parameter error: "+e, map[string]any{"parameters_received": args})
}

func sameCategory(a, b string) bool {
	for _, c := range [][]string{distanceUnits, volumeUnits, weightUnits} {
		if slices.Contains(c, a) && slices.Contains(c, b) {
			return true
		}
	}
	return false
}

// convertTemperature is TEMPERATURE_CONVERSIONS (every pair is there).
func convertTemperature(v float64, from, to string) float64 {
	if from == to {
		return v
	}
	switch from + ">" + to {
	case "celsius>fahrenheit":
		return (v * 9 / 5) + 32
	case "celsius>kelvin":
		return v + 273.15
	case "fahrenheit>celsius":
		return (v - 32) * 5 / 9
	case "fahrenheit>kelvin":
		return (v-32)*5/9 + 273.15
	case "kelvin>celsius":
		return v - 273.15
	}
	return (v-273.15)*9/5 + 32 // kelvin>fahrenheit
}

// unitAliases are _UNIT_ALIASES in the dict's order. "k" appears twice there, so it means
// kelvin (the last value) at kilometers' place (the first key).
var unitAliases = [][2]string{
	{"meter", "meters"}, {"meters", "meters"}, {"metre", "meters"}, {"metres", "meters"}, {"m", "meters"},
	{"kilometer", "kilometers"}, {"kilometers", "kilometers"}, {"kilometre", "kilometers"},
	{"kilometres", "kilometers"}, {"km", "kilometers"}, {"kms", "kilometers"}, {"k", "kelvin"},
	{"centimeter", "centimeters"}, {"centimeters", "centimeters"}, {"cm", "centimeters"},
	{"millimeter", "millimeters"}, {"millimeters", "millimeters"}, {"mm", "millimeters"},
	{"mile", "miles"}, {"miles", "miles"},
	{"yard", "yards"}, {"yards", "yards"}, {"yd", "yards"},
	{"foot", "feet"}, {"feet", "feet"}, {"ft", "feet"},
	{"inch", "inches"}, {"inches", "inches"}, {"in", "inches"},
	{"league", "leagues"}, {"leagues", "leagues"},
	{"liter", "liters"}, {"liters", "liters"}, {"litre", "liters"}, {"litres", "liters"}, {"l", "liters"},
	{"milliliter", "milliliters"}, {"milliliters", "milliliters"}, {"ml", "milliliters"},
	{"gallon", "gallons"}, {"gallons", "gallons"}, {"gal", "gallons"},
	{"quart", "quarts"}, {"quarts", "quarts"}, {"qt", "quarts"},
	{"pint", "pints"}, {"pints", "pints"}, {"pt", "pints"},
	{"cup", "cups"}, {"cups", "cups"},
	{"tablespoon", "tablespoons"}, {"tablespoons", "tablespoons"}, {"tbsp", "tablespoons"},
	{"teaspoon", "teaspoons"}, {"teaspoons", "teaspoons"}, {"tsp", "teaspoons"},
	{"fluid ounce", "fluid_ounces"}, {"fluid ounces", "fluid_ounces"}, {"fl oz", "fluid_ounces"},
	{"gram", "grams"}, {"grams", "grams"}, {"g", "grams"},
	{"kilogram", "kilograms"}, {"kilograms", "kilograms"}, {"kg", "kilograms"}, {"kgs", "kilograms"},
	{"milligram", "milligrams"}, {"milligrams", "milligrams"}, {"mg", "milligrams"},
	{"pound", "pounds"}, {"pounds", "pounds"}, {"lb", "pounds"}, {"lbs", "pounds"},
	{"ounce", "ounces"}, {"ounces", "ounces"}, {"oz", "ounces"},
	{"ton", "tons"}, {"tons", "tons"},
	{"metric ton", "metric_tons"}, {"metric tons", "metric_tons"}, {"tonne", "metric_tons"}, {"tonnes", "metric_tons"},
	{"celsius", "celsius"}, {"centigrade", "celsius"}, {"c", "celsius"},
	{"fahrenheit", "fahrenheit"}, {"f", "fahrenheit"},
	{"kelvin", "kelvin"},
}

// unitGroup is _UNIT_GROUP: the aliases longest first (a stable sort, as Python's), so the
// alternation tries "fluid ounces" before "fl oz" before "in".
var unitGroup = func() string {
	toks := make([]string, len(unitAliases))
	for i, a := range unitAliases {
		toks[i] = a[0]
	}
	slices.SortStableFunc(toks, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	for i, t := range toks {
		toks[i] = regexp.QuoteMeta(t)
	}
	return "(?:" + strings.Join(toks, "|") + ")"
}()

func canonicalUnit(raw string) string {
	norm := spacesRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(raw)), " ")
	for _, a := range unitAliases {
		if a[0] == norm {
			return a[1]
		}
	}
	return ""
}

var (
	convertRe = regexp.MustCompile(`(?i)^\s*convert\s+(?P<value>-?\d+(?:\.\d+)?)\s+(?P<from>` + unitGroup +
		`)\s+(?:to|into|in)\s+(?P<to>` + unitGroup + `)\s*[?.!]*$`)
	howManyRe = regexp.MustCompile(`(?i)^\s*how\s+many\s+(?P<to>` + unitGroup + `)\s+(?:are\s+)?in\s+(?:a\s+|an\s+|(?P<value>-?\d+(?:\.\d+)?)\s+)(?P<from>` +
		unitGroup + `)\s*[?.!]*$`)
	whatsInRe = regexp.MustCompile(`(?i)^\s*(?:what(?:'?s|\s+is))?\s*(?P<value>-?\d+(?:\.\d+)?)\s+(?:degrees?\s+)?(?P<from>` + unitGroup +
		`)\s+in\s+(?:degrees?\s+)?(?P<to>` + unitGroup + `)\s*[?.!]*$`)
)

// unitsMatch is the _fp_* handlers: both units known, the value (1 when "a"/"an").
func unitsMatch(re *regexp.Regexp) func(m []string) (map[string]any, bool) {
	return func(m []string) (map[string]any, bool) {
		from, to := canonicalUnit(group(re, m, "from")), canonicalUnit(group(re, m, "to"))
		if from == "" || to == "" {
			return nil, false
		}
		value := 1.0
		if v := group(re, m, "value"); v != "" {
			value = parseNum(v)
		}
		return map[string]any{"value": value, "from_unit": from, "to_unit": to}, true
	}
}

var convertPatterns = []pattern{
	regexPattern("convert_measurement.convert", "Bypass LLM for 'convert N <unit> to <unit>'", "convert 5 miles to kilometers",
		convertRe, unitsMatch(convertRe)),
	regexPattern("convert_measurement.how_many", "Bypass LLM for 'how many X in a Y'", "how many cups in a gallon",
		howManyRe, unitsMatch(howManyRe)),
	regexPattern("convert_measurement.whats_in", "Bypass LLM for 'what's N <unit> in <unit>'", "what's 350 fahrenheit in celsius",
		whatsInRe, unitsMatch(whatsInRe)),
}

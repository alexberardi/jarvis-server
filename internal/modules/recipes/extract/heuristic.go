package extract

import (
	"regexp"
	"slices"
	"strings"

	"golang.org/x/net/html"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
)

var (
	recipeClass      = regexp.MustCompile(`(?i)recipe|post|content`)
	heuristicQtyLine = regexp.MustCompile(`(?i)\d|\b(cup|tsp|tbsp|tablespoon|teaspoon|ounce|gram|kg|ml|l)\b`)
	instructionHead  = regexp.MustCompile(`(?i)direction|instruction|method`)
)

func liTexts(n *html.Node) []string {
	var out []string
	for _, li := range findAll(n, byTag("li")) {
		out = append(out, text(li, " ", true))
	}
	return out
}

func cleanAll(items []string) []string {
	out := []string{}
	for _, s := range items {
		if c := quantity.CleanText(s); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// Heuristic is extract_recipe_heuristic: the title from <h1> (else <title>); in the article
// (else main, else a recipe/post/content class, else body), the first list where at least
// max(2, n/2) items look like quantities is the ingredients, and the list after a
// "direction|instruction|method" heading (else the first <ol>) the steps.
func Heuristic(markup, url string) *Recipe {
	d := parse(markup)
	var title string
	if t := d.title(); t != nil {
		title = quantity.CleanText(text(t, "", false))
	}
	var container *html.Node
	for _, m := range []func(*html.Node) bool{byTag("article"), byTag("main"), byClassRe(recipeClass)} {
		if container = d.find(m); container != nil {
			break
		}
	}
	if container == nil {
		container = d.body()
	}
	if container == nil {
		return nil
	}
	var best []string
	for _, l := range findAll(container, byTag("ul", "ol")) {
		items := liTexts(l)
		if len(items) < 2 {
			continue
		}
		matches := 0
		for _, it := range items {
			if heuristicQtyLine.MatchString(it) {
				matches++
			}
		}
		if matches >= max(2, len(items)/2) {
			best = items
			break
		}
	}
	var ingredients []quantity.Ingredient
	for _, i := range best {
		if c := quantity.CleanText(i); c != "" {
			ingredients = append(ingredients, quantity.Ingredient{Text: c})
		}
	}

	var steps []string
	if h := findFirst(container, isString(instructionHead)); h != nil && h.Parent != nil {
		if sib := nextSibling(h.Parent, "ol", "ul", "p", "div"); sib != nil {
			if sib.Data == "ol" || sib.Data == "ul" {
				steps = liTexts(sib)
			} else {
				for _, p := range findAll(sib, byTag("p")) {
					steps = append(steps, text(p, " ", true))
				}
				if len(steps) == 0 {
					steps = []string{text(sib, " ", true)}
				}
			}
		}
	}
	if len(steps) == 0 {
		if ols := findAll(container, byTag("ol")); len(ols) > 0 {
			steps = liTexts(ols[0])
		}
	}
	steps = cleanAll(steps)
	servings := quantity.ParseServingsFromText(text(container, " ", true))
	if title == "" || len(ingredients) == 0 || len(steps) == 0 {
		return nil
	}
	return &Recipe{
		Title: title, SourceURL: &url, Tags: []string{}, Servings: servings,
		Ingredients: ingredients, Steps: steps, Notes: []string{},
	}
}

var (
	ingredientLine = regexp.MustCompile(`(?i)\d|\b(cup|tsp|tbsp|tablespoon|teaspoon|ounce|oz|gram|kg|ml|l)\b`)
	actionVerb     = regexp.MustCompile(`(?i)\b(cook|bake|add|mix|stir|heat|pour|season|chop|slice|dice|mince|preheat)\b`)
	instructionPat = []*regexp.Regexp{
		regexp.MustCompile(`(?i)how\s+to\s+make`), regexp.MustCompile(`(?i)instructions?`),
		regexp.MustCompile(`(?i)directions?`), regexp.MustCompile(`(?i)method`),
		regexp.MustCompile(`(?i)steps?`), regexp.MustCompile(`(?i)preparation`),
	}
	stepHeading = regexp.MustCompile(`(?i)^(step\s+\d+|cook|bake|make|prep|prepare|season|add|mix|stir|heat|pour)`)
)

// ingredientItems is heuristic._find_ingredient_items: the list scoring highest on
// quantity-looking lines (2 per match + 1 per item).
func ingredientItems(container *html.Node) []string {
	var best []string
	bestScore := -1
	for _, l := range findAll(container, byTag("ul", "ol")) {
		items := liTexts(l)
		if len(items) < 2 {
			continue
		}
		matches := 0
		for _, it := range items {
			if ingredientLine.MatchString(it) {
				matches++
			}
		}
		if score := matches*2 + len(items); score > bestScore {
			bestScore, best = score, items
		}
	}
	return cleanAll(best)
}

// instructionItems is heuristic._find_instruction_items: the best-scoring <ol> of 3+ items, else
// the block after an instructions-like heading, else step-like headings, else the longest <ol>.
func instructionItems(container *html.Node) []string {
	var steps []string
	if ols := findAll(container, byTag("ol")); len(ols) > 0 {
		var bestList *html.Node
		bestScore := 0
		for _, ol := range ols {
			items := liTexts(ol)
			score := len(items)
			for _, it := range items {
				if actionVerb.MatchString(it) {
					score += 2
				}
			}
			if score > bestScore && len(items) >= 3 {
				bestScore, bestList = score, ol
			}
		}
		if bestList != nil {
			return cleanAll(liTexts(bestList))
		}
	}
	for _, pat := range instructionPat {
		h := findFirst(container, isString(pat))
		if h == nil || h.Parent == nil {
			continue
		}
		if sib := nextSibling(h.Parent, "ol", "ul", "div", "section"); sib != nil {
			if sib.Data == "ol" || sib.Data == "ul" {
				steps = liTexts(sib)
			} else if ps := findAll(sib, byTag("p")); len(ps) > 0 {
				for _, p := range ps {
					steps = append(steps, text(p, " ", true))
				}
			} else if lis := findAll(sib, byTag("li")); len(lis) > 0 {
				steps = liTexts(sib)
			} else {
				for _, hd := range findAll(sib, byTag("h2", "h3", "h4", "strong", "b")) {
					st := text(hd, " ", true)
					if next := nextSibling(hd); next != nil && !slices.Contains([]string{"h2", "h3", "h4", "strong", "b"}, next.Data) {
						st += " " + text(next, " ", true)
					}
					if st != "" && len([]rune(st)) > 10 {
						steps = append(steps, st)
					}
				}
			}
		}
		if len(steps) > 0 {
			break
		}
	}
	if len(steps) == 0 {
		for _, s := range findAll(container, isString(stepHeading)) {
			p := s.Parent
			if p == nil {
				continue
			}
			content := text(p, " ", true)
			if next := nextSibling(p); next != nil {
				content += " " + text(next, " ", true)
			}
			if content != "" && len([]rune(content)) > 20 {
				steps = append(steps, content)
			}
		}
	}
	if len(steps) == 0 {
		var longest *html.Node
		n := -1
		for _, ol := range findAll(container, byTag("ol")) {
			if c := len(findAll(ol, byTag("li"))); c > n {
				longest, n = ol, c
			}
		}
		if longest != nil {
			steps = liTexts(longest)
		}
	}
	return cleanAll(steps)
}

// WebviewHTML builds the page the ingestion's HTML path parses from a webview payload: the
// JSON-LD blocks as <script type="application/ld+json"> tags, then the snippet cleaned of
// boilerplate and cut to its main node (else <body>), at most 100 000 characters when it is
// over 100 000 bytes. Empty when there is neither.
func WebviewHTML(blocks []string, snippet string) string {
	var parts []string
	if len(blocks) > 0 {
		parts = append(parts, JSONLDScripts(blocks))
	}
	if snippet != "" {
		d := parse(snippet)
		cleanForContent(d)
		var cleaned string
		if main := findMainNode(d); main != nil {
			cleaned = render(main)
		} else {
			cleaned = render(d.root)
		}
		if len(cleaned) > 100_000 {
			cleaned = truncRunes(cleaned, 100_000)
		}
		parts = append(parts, cleaned)
	}
	return strings.Join(parts, "\n")
}

// JSONLDScripts is ingestion_service._load_jsonld_blocks: the first 10 blocks of at most
// 200 000 bytes, each wrapped in a JSON-LD script tag.
func JSONLDScripts(blocks []string) string {
	var out []string
	for i, b := range blocks {
		if i == MaxJSONLDBlocks {
			break
		}
		if len(b) > MaxJSONLDBytes {
			continue
		}
		out = append(out, `<script type="application/ld+json">`+b+`</script>`)
	}
	return strings.Join(out, "\n")
}

// Webview payload limits (ingestion_service).
const (
	MaxJSONLDBlocks = 10
	MaxJSONLDBytes  = 200_000
	MaxHTMLBytes    = 400_000
	MaxImages       = 8
	MaxImageBytes   = 8_000_000
)

func truncRunes(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

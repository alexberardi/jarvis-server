package builtins

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// chatTimeout bounds one model reply, which holds up the voice turn.
const chatTimeout = 30 * time.Second

// chatText is the SDK's chat_text (N:JarvisCommandCenterClient.chat_text): the prompt as one
// system message to the live model, at temperature 0, through jarvisd's node LLM passthrough.
func chatText(ctx context.Context, cc *node.CC, prompt string) (string, error) {
	if cc == nil {
		return "", errors.New("not connected to command-center")
	}
	var resp struct {
		Content *string `json:"content"`
	}
	body := map[string]any{"messages": []map[string]string{{"role": "system", "content": prompt}}, "model": "live", "temperature": 0}
	if err := cc.WithTimeout(chatTimeout).Do(ctx, http.MethodPost, "/api/v0/node/llm/chat", body, &resp); err != nil {
		return "", err
	}
	if resp.Content == nil {
		return "", errors.New("the model's reply has no content")
	}
	return *resp.Content, nil
}

// --- answer_question and chat: the model answers from the conversation ---

// Talk is answer_question and chat (commands/answer_question_command.py, chat_command.py):
// they hand the question or the message back to the model to answer.
type Talk struct{}

func (Talk) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{def("answer_question"), def("chat")}
}

func (Talk) Call(_ context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	if name != "answer_question" && name != "chat" {
		return nil, false
	}
	if out := validated(def(name), args); out != nil {
		return out, true
	}
	if name == "chat" {
		msg, _ := args["message"].(string)
		if msg == "" {
			return failure("Message parameter is required", map[string]any{}), true
		}
		return finish(map[string]any{"message": msg}, true, true), true
	}
	said := req.VoiceCommand
	if said == "" {
		said = "[desktop]" // the sidecar's RequestInformation default
	}
	query, ok := args["query"]
	if !ok {
		query = said
	}
	return finish(map[string]any{"voice_command": said, "query": query}, true, true), true
}

// --- tell_joke ---

// Joke is tell_joke (commands/tell_a_joke_command.py): the live model riffs one joke in the
// style of two from the bank, about the topic if there is one; the bank answers when the model
// can't. Its fast paths ("tell me a joke", "tell me a dad joke") run with no other model turn.
type Joke struct {
	CC    CC
	Store *nodesettings.Store // the fast-path switches; nil: all on
}

const jokeName = "tell_joke"

func (j *Joke) Available(context.Context) []node.ToolDef { return []node.ToolDef{def(jokeName)} }

func (j *Joke) Call(ctx context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	if name != jokeName {
		return nil, false
	}
	if out := validated(def(name), args); out != nil {
		return out, true
	}
	topic, _ := args["topic"].(string)
	examples := jokeExamples(topic, 2)
	composed := ""
	if raw, err := chatText(ctx, j.CC(), jokePrompt(topic, examples)); err == nil {
		if c := cleanJoke(raw); len([]rune(c)) >= 12 { // shorter is a broken reply, not a joke
			composed = c
		}
	}
	fallback := composed == ""
	if fallback {
		var unused []string
		for _, b := range jokeBank {
			shown := false
			for _, e := range examples {
				shown = shown || e == b.joke
			}
			if !shown {
				unused = append(unused, b.joke)
			}
		}
		composed = unused[rand.IntN(len(unused))]
	}
	var t any = "random"
	if topic != "" {
		t = topic
	}
	shown := make([]any, len(examples))
	for i, e := range examples {
		shown[i] = e
	}
	return finish(map[string]any{"message": composed, "joke": composed, "topic": t, "style_examples": shown,
		"fallback_used": fallback}, true, false), true
}

func (j *Joke) Direct(ctx context.Context, said string) (map[string]any, bool) {
	return directBy(ctx, j.Store, jokeName, jokePatterns, said, func(a map[string]any) map[string]any {
		out, _ := j.Call(ctx, jokeName, a, node.Request{})
		return out
	})
}

func (j *Joke) FastPaths(command string) []nodesettings.FastPath {
	if command != jokeName {
		return nil
	}
	return patternList(jokePatterns)
}

// JokeMatch is the arguments Direct would tell a joke with (for comparing with N:'s pre_route).
func JokeMatch(said string) (Match, bool) {
	for _, p := range jokePatterns {
		if args, ok := p.match(said); ok {
			return Match{jokeName, args}, true
		}
	}
	return Match{}, false
}

type bankJoke struct {
	joke string
	tags []string
}

var jokeBank = []bankJoke{
	{"Why don't scientists trust atoms? Because they make up everything.", []string{"qa", "science", "general"}},
	{"Why don't skeletons fight each other? They don't have the guts.", []string{"qa", "general"}},
	{"Why did the scarecrow win an award? Because he was outstanding in his field.", []string{"qa", "general", "work"}},
	{"Why don't oysters share their pearls? Because they're shellfish.", []string{"qa", "animals", "ocean"}},
	{"Why did the bicycle fall over? Because it was two tired.", []string{"qa", "general", "wordplay"}},
	{"Why did the math book look sad? Because it had too many problems.", []string{"qa", "math", "school"}},
	{"Why don't programmers like nature? It has too many bugs.", []string{"qa", "technology", "tech", "programming", "nature"}},
	{"Why did the cookie go to the doctor? Because it was feeling crummy.", []string{"qa", "food"}},
	{"Why did the golfer wear two pairs of pants? In case he got a hole in one.", []string{"qa", "sports"}},
	{"Why are baseball games at night? Because the bats sleep during the day.", []string{"qa", "sports", "animals"}},
	{"What do you call a fish with no eyes? A fsh.", []string{"what-do-you-call", "animals", "wordplay"}},
	{"What do you call a bear with no teeth? A gummy bear.", []string{"what-do-you-call", "animals"}},
	{"What do you call a fake noodle? An impasta.", []string{"what-do-you-call", "food"}},
	{"What do you call cheese that isn't yours? Nacho cheese.", []string{"what-do-you-call", "food"}},
	{"What do you call a belt made of watches? A waist of time.", []string{"what-do-you-call", "wordplay", "general"}},
	{"What do you call a sleeping bull? A bulldozer.", []string{"what-do-you-call", "animals"}},
	{"I told my wife she was drawing her eyebrows too high. She looked surprised.", []string{"one-liner", "general", "dad"}},
	{"I'm reading a book about anti-gravity. It's impossible to put down.", []string{"one-liner", "general", "science", "books"}},
	{"I used to play piano by ear. Now I use my hands.", []string{"one-liner", "music", "general"}},
	{"I tried to catch some fog yesterday. Mist.", []string{"one-liner", "weather", "general"}},
	{"I would tell you a construction joke, but I'm still working on it.", []string{"one-liner", "work", "general"}},
	{"Parallel lines have so much in common. It's a shame they'll never meet.", []string{"one-liner", "math", "general"}},
	{"Time flies like an arrow. Fruit flies like a banana.", []string{"one-liner", "wordplay", "general"}},
	{"I'm on a seafood diet. I see food and I eat it.", []string{"one-liner", "food", "general"}},
	{"Knock knock. Who's there? Lettuce. Lettuce who? Lettuce in, it's cold out here.", []string{"knock-knock", "knock knock"}},
	{"Knock knock. Who's there? Boo. Boo who? Don't cry, it's just a joke.", []string{"knock-knock", "knock knock"}},
	{"Knock knock. Who's there? Tank. Tank who? You're welcome.", []string{"knock-knock", "knock knock"}},
	{"I don't trust stairs. They're always up to something.", []string{"dad", "dad jokes", "one-liner", "general"}},
	{"Did you hear about the restaurant on the moon? Great food, no atmosphere.", []string{"dad", "dad jokes", "food", "space"}},
	{"I'm afraid for the calendar. Its days are numbered.", []string{"dad", "dad jokes", "general"}},
	{"What did the ocean say to the shore? Nothing, it just waved.", []string{"dad", "dad jokes", "qa", "nature", "ocean"}},
}

// jokeExamples is _select_examples: count from the jokes tagged with the topic, topped up with
// any others (so the model sees the topic and a variety of shapes).
func jokeExamples(topic string, count int) []string {
	var matches []string
	if needle := strings.ToLower(strings.TrimSpace(topic)); topic != "" {
		for _, j := range jokeBank {
			for _, t := range j.tags {
				if strings.Contains(t, needle) || strings.Contains(needle, t) {
					matches = append(matches, j.joke)
					break
				}
			}
		}
	}
	pick := func(from []string, n int) []string {
		from = append([]string(nil), from...)
		rand.Shuffle(len(from), func(a, b int) { from[a], from[b] = from[b], from[a] })
		return from[:min(n, len(from))]
	}
	if len(matches) >= count {
		return pick(matches, count)
	}
	var rest []string
	for _, j := range jokeBank {
		in := false
		for _, m := range matches {
			in = in || m == j.joke
		}
		if !in {
			rest = append(rest, j.joke)
		}
	}
	return append(matches, pick(rest, count-len(matches))...)
}

// jokePrompt is _build_riff_prompt.
func jokePrompt(topic string, examples []string) string {
	clause := "on any clever subject"
	if topic != "" {
		clause = "about " + topic
	}
	var numbered []string
	for i, e := range examples {
		numbered = append(numbered, string(rune('1'+i))+". "+e)
	}
	return "/no_think\n" +
		"Generate ONE original, clean, family-friendly joke " + clause + ".\n\n" +
		"Style references — match their structure (Q&A, one-liner pun, knock-knock, etc.), " +
		"length, and tone. DO NOT copy or paraphrase any of these jokes:\n" +
		strings.Join(numbered, "\n") + "\n\n" +
		"Requirements:\n" +
		"- Be witty and surprising. Lean on wordplay or a clever twist.\n" +
		"- Keep it short: one to three short sentences.\n" +
		"- Reply with ONLY the joke text. No preface like \"Here's a joke:\", " +
		"no explanation, no commentary."
}

var (
	jokePreambleRe = regexp.MustCompile(`(?i)^(here'?s\s+(a|another|one|your)\s+(clean\s+|family[-\s]?friendly\s+|fresh\s+|new\s+)?joke[:\-\.,]?\s*|sure[!,\.]?\s*|okay[!,\.]?\s*|alright[!,\.]?\s*|how\s+about\s+this[:\-\.,]?\s*|try\s+this\s+one[:\-\.,]?\s*)`)
	jokeThinkRe    = regexp.MustCompile(`(?is)<think>.*?</think>\s*`)
	jokeQuotesRe   = regexp.MustCompile("^[\"'`]+|[\"'`]+$")
)

// cleanJoke is _clean_llm_output: no think block, up to three preambles off, no wrapping quotes.
func cleanJoke(raw string) string {
	text := strings.TrimSpace(jokeThinkRe.ReplaceAllString(raw, ""))
	for range 3 {
		stripped := strings.TrimSpace(jokePreambleRe.ReplaceAllString(text, ""))
		if stripped == text {
			break
		}
		text = stripped
	}
	return strings.TrimSpace(jokeQuotesRe.ReplaceAllString(text, ""))
}

var (
	jokeBareRe  = regexp.MustCompile(`(?i)^\s*(?:tell\s+me\s+(?:another\s+|a\s+)?joke|tell\s+me\s+a\s+(?:another\s+)?(?:clean\s+|family[\-\s]friendly\s+)?joke|make\s+me\s+laugh|say\s+something\s+funny|got\s+(?:any\s+|a\s+)?jokes?|i\s+want\s+a\s+joke|tell\s+me\s+a\s+dad\s+joke|give\s+me\s+(?:a\s+)?(?:dad\s+|knock[\s\-]?knock\s+)?joke)\s*[?.!]*$`)
	jokeTopicRe = regexp.MustCompile(`(?i)^\s*(?:tell\s+me\s+(?:another\s+|a\s+)?joke\s+about|got\s+(?:any\s+|a\s+)?jokes?\s+about|make\s+me\s+laugh\s+with\s+(?:a\s+)?joke\s+about|i\s+want\s+a\s+joke\s+about|give\s+me\s+a\s+joke\s+about)\s+(?P<topic>[a-zA-Z][a-zA-Z\s\-]+?)\s*[?.!]*$`)
	jokeStyleRe = regexp.MustCompile(`(?i)^\s*(?:tell\s+me\s+a\s+(?P<style>dad|knock[\s\-]?knock)\s+joke|give\s+me\s+a\s+(?P<style2>dad|knock[\s\-]?knock)\s+joke)\s*[?.!]*$`)
)

var jokePatterns = []pattern{
	regexPattern("tell_joke.style", "Bypass LLM for 'tell me a dad joke' / 'give me a knock-knock joke'", "tell me a dad joke",
		jokeStyleRe, func(m []string) (map[string]any, bool) {
			style := strings.ToLower(strings.TrimSpace(group(jokeStyleRe, m, "style") + group(jokeStyleRe, m, "style2")))
			switch {
			case strings.Contains(style, "knock"):
				return map[string]any{"topic": "knock knock"}, true
			case strings.Contains(style, "dad"):
				return map[string]any{"topic": "dad jokes"}, true
			}
			return nil, false
		}),
	regexPattern("tell_joke.topic", "Bypass LLM for 'tell me a joke about <topic>'", "tell me a joke about cats",
		jokeTopicRe, func(m []string) (map[string]any, bool) {
			t := strings.TrimSpace(group(jokeTopicRe, m, "topic"))
			return map[string]any{"topic": t}, t != ""
		}),
	regexPattern("tell_joke.bare", "Bypass LLM for bare joke requests ('tell me a joke', 'make me laugh')", "tell me a joke",
		jokeBareRe, func([]string) (map[string]any, bool) { return map[string]any{}, true }),
}

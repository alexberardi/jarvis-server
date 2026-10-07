package textfilter

// services/acknowledgment_service.py: the instant, LLM-free acknowledgment spoken when the main
// response is slow. Changed by M5: keywords match on word boundaries ("show" no longer matches
// "how", "train" no longer matches "rain").

type ackPool struct {
	re   pyre
	pool []string
}

var ackPools = []ackPool{
	{mustPyI(`\b(?:weather|forecast|temperature|rain|umbrella)\b`), []string{
		"Checking the forecast.",
		"Let me check the weather.",
		"One moment, pulling up the forecast.",
	}},
	{mustPyI(`\b(?:timer|alarm|remind)\b`), []string{
		"Setting that up.",
		"On it.",
		"Got it.",
	}},
	{mustPyI(`\b(?:light|lamp|switch|plug|turn on|turn off)\b`), []string{
		"On it.",
		"Sure thing.",
		"Right away.",
	}},
	{mustPyI(`\b(?:play|pause|stop|music|song|volume|speaker)\b`), []string{
		"Sure.",
		"On it.",
		"You got it.",
	}},
	{mustPyI(`\b(?:search|look up|find|who|what|when|where|why|how)\b`), []string{
		"Let me look into that.",
		"Good question, give me a moment.",
		"Looking into it.",
		"Let me find out.",
	}},
	{mustPyI(`\b(?:recipe|cook|ingredient|meal)\b`), []string{
		"Let me pull that up.",
		"Checking on that.",
		"One moment.",
	}},
	{mustPyI(`\b(?:news|headline|happening)\b`), []string{
		"Let me check.",
		"Pulling up the latest.",
		"One moment.",
	}},
	{mustPyI(`\b(?:score|game|sport)\b`), []string{
		"Let me check the scores.",
		"Checking on that.",
		"One moment.",
	}},
}

// ackGeneric is _GENERIC_POOL.
var ackGeneric = []string{
	"Let me look into that.",
	"Working on it.",
	"One moment.",
	"Give me a second.",
	"On it.",
	"Let me check.",
}

// Acknowledgment is generate_acknowledgment: the first keyword pool (in legacy order) whose
// keywords appear as whole words in command, else the generic pool; pick(n) chooses an index
// in [0, n) (random.choice in legacy; math/rand/v2.IntN in production).
func Acknowledgment(command string, pick func(n int) int) string {
	for _, p := range ackPools {
		if p.re.search(command) {
			return p.pool[pick(len(p.pool))]
		}
	}
	return ackGeneric[pick(len(ackGeneric))]
}

// AckPool returns the pool Acknowledgment draws from for command (for tests and callers that
// want the candidates).
func AckPool(command string) []string {
	for _, p := range ackPools {
		if p.re.search(command) {
			return p.pool
		}
	}
	return ackGeneric
}

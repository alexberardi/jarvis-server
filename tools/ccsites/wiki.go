package main

import (
	"net/http"
	"strings"
)

// The wiki: encyclopedia-style articles for summarizing tasks (t1_writer_summary). The places are
// made up, so a summary can only come from the page, never from what a model remembers.
type article struct {
	Title    string
	Lead     string
	Sections []section
}

type section struct {
	Heading string
	Paras   []string
}

var articles = map[string]article{
	"Velmora": {
		Title: "Velmora",
		Lead: "Velmora is a lakeside town on the northern shore of Lake Orrin, in the Hallen Valley. " +
			"It was founded in 1287 by salt traders and had a population of 41,300 at the last census. " +
			"The town is known for the Tessaly Bridge, its cormorant fishing, and the Brassfeather Festival held every September.",
		Sections: []section{
			{"History", []string{
				"Salt traders from the coast founded Velmora in 1287 as a resting place on the road to the Hallen mines. " +
					"The market charter granted in 1312 made the town the valley's main salt market, and salt stayed its chief trade for four centuries.",
				"A fire in 1644 destroyed most of the wooden old town. It was rebuilt in grey lakestone, which gives the centre its uniform look today. " +
					"The arrival of the railway in 1871 ended the salt road, and the town turned to fishing and, later, tourism.",
			}},
			{"Geography", []string{
				"Velmora lies at 412 metres above sea level between Lake Orrin and the wooded Carrow Hills. " +
					"Lake Orrin is 31 kilometres long and freezes over in about one winter in five. " +
					"The river Esk leaves the lake at the town's western edge, where the Tessaly Bridge crosses it.",
			}},
			{"Economy", []string{
				"Tourism is the largest employer, followed by boat building and a cooperative of lake fisheries. " +
					"Cormorant fishing, in which trained birds catch fish for their handlers, survives as a working tradition kept by eleven families.",
			}},
			{"Culture", []string{
				"The Brassfeather Festival, held on the second weekend of September since 1702, ends the fishing season with a parade of boats " +
					"decorated with brass feathers and a night of lanterns on the lake. It draws around 60,000 visitors.",
				"The Tessaly Bridge, a seven-arch stone bridge finished in 1409, is the oldest standing structure in the town and appears on its coat of arms.",
			}},
			{"Climate", []string{
				"Velmora has a cool lake climate. Summers are mild, with July averaging 19 °C, and winters are cold and damp, with January averaging -3 °C. " +
					"Fog from the lake covers the lower town on about ninety mornings a year, mostly in October and November. " +
					"Most of the 860 millimetres of yearly rain falls in late summer storms that come over the Carrow Hills.",
			}},
			{"Transport", []string{
				"Velmora station is on the Hallen Valley line, with hourly trains to the regional capital, Brask, a journey of 52 minutes. " +
					"A ferry crosses Lake Orrin to the village of Oddby four times a day from April to October; in winter a bus takes the long road around the lake.",
				"The old salt road survives as a cycle path between Velmora and the Hallen mines, now a museum. " +
					"Cars have been kept out of the old town since 1989, and the Tessaly Bridge has been open only to people on foot and bicycles since 1994.",
			}},
			{"Government", []string{
				"The town is governed by a council of nineteen members elected every four years, which chooses the mayor from among its members. " +
					"Since 2019 the council has met in the restored Salt Hall on the market square, built in 1652 after the fire as the salt merchants' guild house.",
			}},
			{"Notable people", []string{
				"Ilse Marrow (1803–1871), the painter whose views of Lake Orrin made the town known to travellers, was born in Velmora. " +
					"Other natives include the boat designer Oskar Fenn (1898–1960), whose flat-bottomed lake boats are still built in the town's yards, " +
					"and the folklorist Dagny Holt (1921–2008), who wrote the standard history of the Brassfeather Festival.",
			}},
		},
	},
}

// wiki serves /wiki/<Title>: one article in <main>, as an encyclopedia's page.
func (s *Server) wiki(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/wiki/")
	a, ok := articles[name]
	if !ok || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, http.StatusOK, "wiki", a.Title+" - Test Wiki", a)
}

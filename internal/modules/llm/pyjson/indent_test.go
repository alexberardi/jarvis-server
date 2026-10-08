package pyjson

import "testing"

func TestDumpsIndent(t *testing.T) {
	v, err := Loads(`{"a": [1, {"b": []}, {}], "c": "é", "d": null, "e": 1.5, "f": []}`)
	if err != nil {
		t.Fatal(err)
	}
	// python3 -c 'json.dumps({...}, indent=2)'
	want := "{\n  \"a\": [\n    1,\n    {\n      \"b\": []\n    },\n    {}\n  ],\n  \"c\": \"\\u00e9\",\n  \"d\": null,\n  \"e\": 1.5,\n  \"f\": []\n}"
	if got := DumpsIndent(v, true, 2); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	type s struct {
		Z string   `json:"z"`
		A []string `json:"a"`
	}
	if got := DumpsIndent(s{Z: "x", A: []string{"y"}}, true, 2); got != "{\n  \"z\": \"x\",\n  \"a\": [\n    \"y\"\n  ]\n}" {
		t.Fatalf("struct order: %q", got)
	}
}

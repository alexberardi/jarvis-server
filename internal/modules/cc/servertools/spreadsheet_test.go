package servertools

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/xuri/excelize/v2"
)

func TestSpreadsheetName(t *testing.T) {
	for in, want := range map[string]string{
		"Budget 2026-09":        "Budget 2026-09",
		" budget.xlsx ":         "budget",
		"Budget: Sept?":         "Budget- Sept-",
		strings.Repeat("a", 90): strings.Repeat("a", maxNameRunes),
	} {
		if got, err := SpreadsheetName(in); err != nil || got != want {
			t.Errorf("SpreadsheetName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "  ", ".xlsx", "../x", "a/b", `a\b`, "..", "..."} {
		if got, err := SpreadsheetName(bad); err == nil {
			t.Errorf("SpreadsheetName(%q) = %q, want an error", bad, got)
		}
	}
}

func sheetsArg(t *testing.T, s string) any {
	t.Helper()
	v, err := pyjson.Loads(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Rows as lists or as objects keyed by column; amounts become numbers, codes with leading
// zeros stay text; duplicate and invalid titles are fixed rather than refused.
func TestBuildWorkbook(t *testing.T) {
	sheets, err := SheetsFromArgs(sheetsArg(t, `[
		{"title": "Transactions", "columns": ["Date", "Description", "Amount", "Category"],
		 "rows": [["2026-09-01", "Grocer", -54.2, "Groceries"], ["2026-09-02", "Pay", "1200.00", "Income"]]},
		{"title": "Transactions", "rows": [{"Category": "Groceries", "Total": -54.2, "Code": "00123"}]},
		{"title": "a/b[c]", "columns": ["x"], "rows": []}]`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := BuildWorkbook(sheets)
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.GetSheetList(), "|"); got != "Transactions|Transactions (2)|a-b-c-" {
		t.Fatalf("sheets %q", got)
	}
	rows, _ := f.GetRows("Transactions")
	if len(rows) != 3 || strings.Join(rows[0], ",") != "Date,Description,Amount,Category" || rows[1][2] != "-54.2" {
		t.Fatalf("rows %q", rows)
	}
	if isText(t, f, "Transactions", "C3") || !isText(t, f, "Transactions", "B3") {
		t.Fatal("a quoted amount is text, or a description isn't")
	}
	if v, _ := f.GetCellValue("Transactions", "C3"); v != "1200" {
		t.Fatalf("C3 %q", v)
	}
	rows, _ = f.GetRows("Transactions (2)")
	if len(rows) != 2 || strings.Join(rows[0], ",") != "Category,Total,Code" || rows[1][2] != "00123" {
		t.Fatalf("object rows %q", rows)
	}
	if !isText(t, f, "Transactions (2)", "C2") || isText(t, f, "Transactions (2)", "B2") {
		t.Fatal("a code with a leading zero became a number, or an amount text")
	}
}

// isText is whether a cell holds a string (a number has no type in the file).
func isText(t *testing.T, f *excelize.File, sheet, cell string) bool {
	t.Helper()
	typ, err := f.GetCellType(sheet, cell)
	if err != nil {
		t.Fatal(err)
	}
	return typ == excelize.CellTypeSharedString || typ == excelize.CellTypeInlineString
}

func TestSheetsFromArgsLimits(t *testing.T) {
	for name, s := range map[string]string{
		"none":       `[]`,
		"not a list": `{"title": "x"}`,
		"bad row":    `[{"title": "x", "rows": ["a"]}]`,
		"too many":   `[` + strings.TrimSuffix(strings.Repeat(`{"title": "x"},`, MaxSheets+1), ",") + `]`,
	} {
		if _, err := SheetsFromArgs(sheetsArg(t, s)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := SheetsFromArgs(sheetsArg(t, `[{"title": "x", "rows": [`+strings.TrimSuffix(strings.Repeat(`[1],`, MaxSheetRows+1), ",")+`]}]`)); err == nil {
		t.Error("over the row cap: no error")
	}
	sheet := `{"title": "x", "rows": [` + strings.TrimSuffix(strings.Repeat(`[1,2,3,4,5],`, MaxSheetRows), ",") + `]}`
	if _, err := SheetsFromArgs(sheetsArg(t, `[`+strings.TrimSuffix(strings.Repeat(sheet+",", MaxSheetCells/(5*MaxSheetRows)+1), ",")+`]`)); err == nil {
		t.Error("over the cell cap: no error")
	}
}

// Sheets written as a JSON string (as the house model does in errand steps) are read the same.
func TestSheetsFromArgsJSONString(t *testing.T) {
	sheets, err := SheetsFromArgs(`[{"title": "Totals", "columns": ["Category", "Total"], "rows": [["Dining", 362]]}]`)
	if err != nil || len(sheets) != 1 || sheets[0].Title != "Totals" || len(sheets[0].Rows) != 1 {
		t.Fatalf("%+v %v", sheets, err)
	}
	if _, err := SheetsFromArgs(`not json`); err == nil {
		t.Error("a string that isn't JSON: no error")
	}
}

func TestSheetPreview(t *testing.T) {
	var rows [][]any
	for i := 0; i < 15; i++ {
		rows = append(rows, []any{"c" + strings.Repeat("x", i%2), i})
	}
	got := SheetPreview([]Sheet{{Title: "All"}, {Title: "Totals", Rows: rows}})
	if !strings.HasPrefix(got, "Totals: c 0; cx 1; ") || !strings.HasSuffix(got, "c 10; cx 11; and 3 more rows.") {
		t.Fatalf("preview %q", got)
	}
	if got := SheetPreview([]Sheet{{Title: "Empty"}}); got != "" {
		t.Fatalf("empty preview %q", got)
	}
}

package servertools

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/xuri/excelize/v2"
)

// Spreadsheets for write_spreadsheet (plan P3 piece 13): an errand's rows become an .xlsx that
// the computer's agent saves in ~/Documents/Jarvis. The caps keep a workbook well under the
// agent's 5 MiB write limit.

const (
	MaxSheets     = 12
	MaxSheetRows  = 5000
	MaxSheetCells = 100000 // all sheets together
	maxNameRunes  = 80
	maxTitleRunes = 31 // Excel's own limit
	maxCellRunes  = 2000
)

// Sheet is one sheet of a workbook: a header row, then the rows. A cell is a string, a number
// or a bool.
type Sheet struct {
	Title   string
	Columns []string
	Rows    [][]any
}

// SpreadsheetName is a file name (without .xlsx) made from what the model passed: no folders,
// no "..", nothing a file system or Excel refuses.
func SpreadsheetName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if strings.HasSuffix(strings.ToLower(name), ".xlsx") {
		name = name[:len(name)-len(".xlsx")]
	}
	if strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return "", errors.New("the name must be a plain file name, not a path")
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`:*?"<>|`, r) {
			return '-'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		return "", errors.New("the spreadsheet needs a name")
	}
	if r := []rune(name); len(r) > maxNameRunes {
		name = strings.TrimSpace(string(r[:maxNameRunes]))
	}
	return name, nil
}

// SheetsFromArgs reads write_spreadsheet's sheets: [{title, columns, rows}], where a row is a
// list of cells in column order or an object keyed by column (its keys are the columns when
// columns is empty).
func SheetsFromArgs(v any) ([]Sheet, error) {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil, errors.New("sheets must be a list of {title, columns, rows}")
	}
	if len(list) > MaxSheets {
		return nil, fmt.Errorf("at most %d sheets", MaxSheets)
	}
	var out []Sheet
	cells := 0
	for i, e := range list {
		o, ok := e.(*pyjson.Object)
		if !ok {
			return nil, fmt.Errorf("sheet %d is not an object", i+1)
		}
		sh := Sheet{Title: objString(o, "title")}
		cv, _ := o.Get("columns")
		cols, _ := cv.([]any)
		for _, c := range cols {
			sh.Columns = append(sh.Columns, cellText(c))
		}
		rv, _ := o.Get("rows")
		rows, ok := rv.([]any)
		if rv != nil && !ok {
			return nil, fmt.Errorf("sheet %d: rows must be a list", i+1)
		}
		if len(rows) > MaxSheetRows {
			return nil, fmt.Errorf("sheet %d: at most %d rows", i+1, MaxSheetRows)
		}
		for j, r := range rows {
			var row []any
			switch x := r.(type) {
			case []any:
				row = x
			case *pyjson.Object:
				if len(sh.Columns) == 0 {
					sh.Columns = x.Keys()
				}
				for _, c := range sh.Columns {
					v, _ := x.Get(c)
					row = append(row, v)
				}
			default:
				return nil, fmt.Errorf("sheet %d, row %d: a row is a list of cells", i+1, j+1)
			}
			cells += len(row)
			sh.Rows = append(sh.Rows, row)
		}
		cells += len(sh.Columns)
		if cells > MaxSheetCells {
			return nil, fmt.Errorf("at most %d cells in all", MaxSheetCells)
		}
		out = append(out, sh)
	}
	return out, nil
}

// BuildWorkbook writes sheets as an .xlsx: the columns in bold on the first row, numbers as
// numbers (an amount the model quoted, "-12.50", too).
func BuildWorkbook(sheets []Sheet) ([]byte, error) {
	if len(sheets) == 0 {
		return nil, errors.New("no sheets")
	}
	f := excelize.NewFile()
	defer f.Close()
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for i, sh := range sheets {
		title := sheetTitle(sh.Title, i, used)
		if i == 0 {
			if err := f.SetSheetName(f.GetSheetName(0), title); err != nil {
				return nil, err
			}
		} else if _, err := f.NewSheet(title); err != nil {
			return nil, err
		}
		r := 1
		if len(sh.Columns) > 0 {
			head := make([]any, len(sh.Columns))
			for j, c := range sh.Columns {
				head[j] = clip(c)
			}
			if err := f.SetSheetRow(title, "A1", &head); err != nil {
				return nil, err
			}
			end, _ := excelize.CoordinatesToCellName(len(head), 1)
			if err := f.SetCellStyle(title, "A1", end, bold); err != nil {
				return nil, err
			}
			r++
		}
		for _, row := range sh.Rows {
			vals := make([]any, len(row))
			for j, c := range row {
				vals[j] = cellValue(c)
			}
			cell, _ := excelize.CoordinatesToCellName(1, r)
			if err := f.SetSheetRow(title, cell, &vals); err != nil {
				return nil, err
			}
			r++
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sheetTitle is a title Excel takes: no []:*?/\, at most 31 characters, unique.
func sheetTitle(t string, i int, used map[string]bool) string {
	t = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`[]:*?/\`, r) {
			return '-'
		}
		return r
	}, t))
	t = strings.Trim(t, "'")
	if t == "" {
		t = "Sheet " + strconv.Itoa(i+1)
	}
	if r := []rune(t); len(r) > maxTitleRunes {
		t = string(r[:maxTitleRunes])
	}
	base := t
	for n := 2; used[strings.ToLower(t)]; n++ {
		sfx := " (" + strconv.Itoa(n) + ")"
		r := []rune(base)
		if len(r)+len(sfx) > maxTitleRunes {
			r = r[:maxTitleRunes-len(sfx)]
		}
		t = string(r) + sfx
	}
	used[strings.ToLower(t)] = true
	return t
}

// plainNumber is a number written plainly: no leading zeros (an account or ZIP code stays text),
// no thousands separators or currency signs.
var plainNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,14})(\.[0-9]{1,10})?$`)

func cellValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case bool, float64, int, int64:
		return x
	case *big.Int:
		if x.IsInt64() {
			return x.Int64()
		}
		return x.String()
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	case string:
		if s := strings.TrimSpace(x); plainNumber.MatchString(s) {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return f
			}
		}
		return clip(x)
	}
	return clip(pyjson.Dumps(v, false))
}

func cellText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return pyjson.Str(v)
}

func clip(s string) string {
	if r := []rune(s); len(r) > maxCellRunes {
		return string(r[:maxCellRunes])
	}
	return s
}

func objString(o *pyjson.Object, k string) string {
	v, _ := o.Get(k)
	if v == nil {
		return ""
	}
	return cellText(v)
}

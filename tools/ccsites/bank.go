package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"hash/fnv"
	"net/http"
	"slices"
	"strings"
)

// months with a statement, newest first: "last month" is 2026-09.
var months = []string{"2026-09", "2026-08", "2026-07", "2026-06", "2026-05", "2026-04"}

// OverBudgetCategory is the one category clearly over budget (at least 1.5×) on every statement,
// bank and card alike; every other category stays under 85% of its budget.
const OverBudgetCategory = "Dining"

// categories with their monthly budget in cents and the merchants a transaction picks from.
var categories = []struct {
	Name      string
	Budget    int
	Merchants []string
}{
	{"Groceries", 60000, []string{"Fresh Market", "Green Grocer", "Corner Foods", "Harvest Co-op"}},
	{"Dining", 20000, []string{"Luigi's Trattoria", "Sushi Den", "The Burger Shed", "Cafe Aroma"}},
	{"Transport", 18000, []string{"City Transit", "QuickFuel", "RideNow", "Parking Plaza"}},
	{"Utilities", 25000, []string{"Springfield Power", "AquaWorks", "FiberNet", "Mobile One"}},
	{"Entertainment", 12000, []string{"StreamFlix", "Cineplex 8", "Tunes+", "Game Vault"}},
	{"Shopping", 30000, []string{"MegaMart", "Book Nook", "Home Goods", "Style Outlet"}},
	{"Health", 15000, []string{"Wellness Pharmacy", "FitLife Gym", "Dr. Patel Clinic", "Vision Plus"}},
	{"Travel", 40000, []string{"SkyHigh Air", "Lakeside Inn", "RailWay", "Trip Booker"}},
}

// Txn is one statement line; Amount is in cents (spending is positive).
type Txn struct {
	Date        string
	Description string
	Category    string
	Amount      int
}

// rng is a small deterministic generator: the same site and month always give the same statement.
type rng uint64

func (r *rng) next(n int) int {
	*r = *r*6364136223846793005 + 1442695040888963407
	return int((uint64(*r) >> 33) % uint64(n))
}

// transactions returns the 40 seeded transactions of site's statement for month (5 per category),
// sorted by date.
func transactions(site, month string) []Txn {
	h := fnv.New64a()
	h.Write([]byte(site + "/" + month))
	r := rng(h.Sum64())
	var out []Txn
	for _, c := range categories {
		// Over-budget category: 160–190% of the budget; the rest 45–80%.
		pct := 45 + r.next(36)
		if c.Name == OverBudgetCategory {
			pct = 160 + r.next(31)
		}
		total := c.Budget * pct / 100
		weights := make([]int, 5)
		sum := 0
		for i := range weights {
			weights[i] = 5 + r.next(16)
			sum += weights[i]
		}
		left := total
		for i, wt := range weights {
			amt := total * wt / sum
			if i == len(weights)-1 {
				amt = left
			}
			left -= amt
			out = append(out, Txn{
				Date:        fmt.Sprintf("%s-%02d", month, 1+r.next(28)),
				Description: c.Merchants[r.next(len(c.Merchants))],
				Category:    c.Name,
				Amount:      amt,
			})
		}
	}
	slices.SortStableFunc(out, func(a, b Txn) int { return strings.Compare(a.Date, b.Date) })
	return out
}

// CategoryTotal is one row of a statement's budget table (cents).
type CategoryTotal struct {
	Category string
	Spent    int
	Budget   int
}

func (c CategoryTotal) Over() bool { return c.Spent > c.Budget }

func categoryTotals(site, month string) []CategoryTotal {
	spent := map[string]int{}
	for _, t := range transactions(site, month) {
		spent[t.Category] += t.Amount
	}
	var out []CategoryTotal
	for _, c := range categories {
		out = append(out, CategoryTotal{Category: c.Name, Spent: spent[c.Name], Budget: c.Budget})
	}
	return out
}

func dollars(c int) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

func statementFile(site, month string) string {
	if site == "card" {
		return "card-statement-" + month + ".csv"
	}
	return "statement-" + month + ".csv"
}

type statementData struct {
	SiteName string
	Month    string
	Months   []string
	Txns     []Txn
	Totals   []CategoryTotal
	Total    int
}

func (s *Server) bank(w http.ResponseWriter, r *http.Request, site string) {
	path := strings.TrimPrefix(r.URL.Path, "/"+site)
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if path == "/" {
		http.Redirect(w, r, keepLayout(r, "/"+site+"/statements"), http.StatusFound)
		return
	}
	if path == "/statements" {
		s.render(w, r, http.StatusOK, "bank_index", walled[site]+": statements", statementData{SiteName: walled[site], Months: months})
		return
	}
	rest, ok := strings.CutPrefix(path, "/statements/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	month, download := strings.CutSuffix(rest, "/download")
	if !slices.Contains(months, month) {
		http.NotFound(w, r)
		return
	}
	txns := transactions(site, month)
	if download {
		var b bytes.Buffer
		cw := csv.NewWriter(&b)
		cw.Write([]string{"date", "description", "category", "amount"})
		for _, t := range txns {
			cw.Write([]string{t.Date, t.Description, t.Category, dollars(t.Amount)})
		}
		cw.Flush()
		name := statementFile(site, month)
		s.mu.Lock()
		s.st.Downloads = append(s.st.Downloads, Download{Site: site, Month: month, Filename: name, Rows: len(txns), Time: s.now().UTC()})
		s.save()
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Write(b.Bytes())
		return
	}
	d := statementData{SiteName: walled[site], Month: month, Months: months, Txns: txns, Totals: categoryTotals(site, month)}
	for _, t := range txns {
		d.Total += t.Amount
	}
	s.render(w, r, http.StatusOK, "bank_statement", walled[site]+": statement "+month, d)
}

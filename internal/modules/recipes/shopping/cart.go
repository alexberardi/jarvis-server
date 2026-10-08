package shopping

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
)

// Cart helpers, ported from grocery_service (docs/recipes/00-inventory.md §4.7);
// fixtures/golden/recipes/cart.json is their contract.

// WalmartCartURL is Walmart's affiliate add-to-cart deep link; items are "<sku>_<qty>" pairs.
const WalmartCartURL = "https://affil.walmart.com/cart/addToCart?items="

// MapKey is grocery_service.map_key: the SKU map's key for a RAW ingredient line. It is
// NormalizeName, which is not idempotent ("90/10 ground beef" → "ground beef"), so it is
// applied only where a raw line enters.
func MapKey(text string) string { return NormalizeName(text) }

// ParsePackSize is _parse_pack_size: `^\s*(\d+(?:\.\d+)?)\s*([a-zA-Z]+)?` over a product's
// package size ("1 lb", "12oz", "6"). Both results are nil when there is no leading number.
func ParsePackSize(unitSize string) (*big.Rat, *string) {
	s := strings.TrimLeftFunc(unitSize, quantity.IsPySpace)
	num, rest := digits(s)
	if num == "" {
		return nil, nil
	}
	if strings.HasPrefix(rest, ".") {
		if frac, r := digits(rest[1:]); frac != "" {
			num, rest = num+"."+frac, r
		}
	}
	size, ok := new(big.Rat).SetString(num)
	if !ok {
		return nil, nil
	}
	rest = strings.TrimLeftFunc(rest, quantity.IsPySpace)
	end := 0
	for end < len(rest) && (rest[end] >= 'a' && rest[end] <= 'z' || rest[end] >= 'A' && rest[end] <= 'Z') {
		end++
	}
	if end == 0 {
		return size, nil
	}
	unit := rest[:end]
	return size, &unit
}

// digits splits a leading run of Unicode decimal digits (Python's \d) off s, as ASCII.
func digits(s string) (string, string) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		d := quantity.DigitValue(r)
		if d < 0 {
			break
		}
		b.WriteByte(byte('0' + d))
		i += n
	}
	return b.String(), s[i:]
}

// PackQuantity is _pack_quantity: how many of the mapped product to buy. When the package size
// parses and an amount's unit matches it (case-insensitively; no unit matches no unit), it is
// ceil(quantity / size); otherwise 1. Never 0.
func PackQuantity(amounts []Amount, unitSize *string) int {
	if unitSize == nil || *unitSize == "" {
		return 1
	}
	size, sizeUnit := ParsePackSize(*unitSize)
	if size == nil || size.Sign() <= 0 {
		return 1
	}
	for _, a := range amounts {
		if a.Quantity == nil {
			continue
		}
		if strings.ToLower(deref(a.Unit)) != strings.ToLower(deref(sizeUnit)) {
			continue
		}
		q, _ := a.Quantity.Float64()
		sz, _ := size.Float64()
		return max(1, int(math.Ceil(q/sz)))
	}
	return 1
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// AmountDisplay is _amount_display: the amounts as a shopper reads them, for the product
// picker's search box. It keeps legacy's formatting bug, frozen by the golden: the quantity is
// rendered as Decimal.normalize() in fixed notation and then has every trailing "0" stripped,
// so 100 g reads "1 g".
func AmountDisplay(amounts []Amount) string {
	var parts []string
	for _, a := range amounts {
		if a.Quantity != nil {
			qty := strings.TrimRight(strings.TrimRight(plainDecimal(a.Quantity), "0"), ".")
			if a.Unit != nil && *a.Unit != "" {
				parts = append(parts, quantity.PyStrip(qty+" "+*a.Unit))
			} else {
				parts = append(parts, qty)
			}
		}
		parts = append(parts, a.Unparsed...)
	}
	kept := parts[:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, ", ")
}

// plainDecimal is f"{d.normalize():f}" for a terminating decimal: fixed notation with no
// trailing fractional zeros.
func plainDecimal(r *big.Rat) string {
	if r.IsInt() {
		return r.Num().String()
	}
	s := r.FloatString(30)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// CartPair is one item of the cart link.
type CartPair struct {
	SKU      string
	Quantity int
}

// CartURL is cart_url: the Walmart deep link for the pairs with a SKU, or nil when there are
// none (an empty items= opens an empty cart that reads as a broken export). The pair list is
// percent-encoded like urllib's quote(..., safe=",").
func CartURL(pairs []CartPair) *string {
	var items []string
	for _, p := range pairs {
		if p.SKU != "" {
			items = append(items, fmt.Sprintf("%s_%d", p.SKU, p.Quantity))
		}
	}
	if len(items) == 0 {
		return nil
	}
	u := WalmartCartURL + quote(strings.Join(items, ","))
	return &u
}

// quote is urllib.parse.quote(s, safe=","): unreserved characters and "," stay, every other
// byte of the UTF-8 encoding is %XX.
func quote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_.-~,", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

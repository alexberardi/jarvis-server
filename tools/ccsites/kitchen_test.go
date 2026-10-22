package main

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The kitchen's shape is the point of it (#485): a menu longer than the snapshot's 300 elements,
// dialogs after all of it, add buttons that open options, upsells in the cart, and leftovers.
func TestKitchen(t *testing.T) {
	s, hs := newSites(t, "")
	c := browser(t)

	_, _, body := get(t, c, hs.URL+"/kitchen/")
	if n := len(regexp.MustCompile(`<a |<button `).FindAllString(body, -1)); n <= 300 {
		t.Fatalf("the menu has %d interactive elements, want more than 300", n)
	}
	if !strings.Contains(body, `aria-label="Cart with 3 items"`) {
		t.Fatal("the cart doesn't start with the leftovers (3 items)")
	}

	// The add button opens the item's options dialog, after the whole menu.
	_, _, body = get(t, c, hs.URL+"/kitchen/item/"+KitchenDish)
	dlg := strings.Index(body, `role="dialog"`)
	if dlg < 0 || dlg < strings.LastIndex(body, "/kitchen/item/") || !strings.Contains(body[dlg:], "Add to Cart $14.00") {
		t.Fatal("no options dialog after the menu")
	}

	// The cart dialog: the leftovers with Remove, the upsells, Checkout.
	_, _, body = get(t, c, hs.URL+"/kitchen/?cart=open")
	for _, want := range []string{"2 × Pulled Pork Sandwich", "1 × Smokehouse Thick Burger", `aria-label="Remove Smokehouse Thick Burger"`,
		"+ Texas Carolina Potato Salad - Pt. $10.00", "Checkout $47.00"} {
		if !strings.Contains(body, want) {
			t.Fatalf("cart dialog lacks %q", want)
		}
	}

	// Clear it down to one sandwich and order: the order holds exactly that.
	post(t, c, hs.URL+"/kitchen/cart/remove", url.Values{"line": {"1"}}) // the burger
	post(t, c, hs.URL+"/kitchen/cart/remove", url.Values{"line": {"0"}}) // the two sandwiches
	code, h, _ := post(t, c, hs.URL+"/kitchen/cart/add", url.Values{"id": {KitchenDish}, "qty": {"1"}, "opt": {"No Pickles"}})
	if code != 303 || h.Get("Location") != "/kitchen/?cart=open" {
		t.Fatalf("add: %d %s", code, h.Get("Location"))
	}
	_, _, body = get(t, c, hs.URL+"/kitchen/checkout")
	if !strings.Contains(body, "1 × Pulled Pork Sandwich (No Pickles) $14.00") || !strings.Contains(body, "Place Order") {
		t.Fatalf("checkout:\n%s", body)
	}
	code, h, _ = post(t, c, hs.URL+"/kitchen/checkout", nil)
	if code != 303 || !strings.HasPrefix(h.Get("Location"), "/kitchen/order/SK-") {
		t.Fatalf("place order: %d %s", code, h.Get("Location"))
	}
	if code, _, _ := get(t, c, hs.URL+h.Get("Location")); code != 200 {
		t.Fatalf("order page: %d", code)
	}
	s.mu.Lock()
	o := s.st.Orders[len(s.st.Orders)-1]
	s.mu.Unlock()
	if len(o.Items) != 1 || o.Items[0].ID != KitchenDish || o.Items[0].Qty != 1 || o.Total != 14 {
		t.Fatalf("order %+v", o)
	}

	// After a reset the leftovers are back.
	c2 := browser(t)
	post(t, c2, hs.URL+"/control/reset", nil)
	if _, _, body := get(t, c2, hs.URL+"/kitchen/"); !strings.Contains(body, "Cart with 3 items") {
		t.Fatal("no leftovers after a reset")
	}
}

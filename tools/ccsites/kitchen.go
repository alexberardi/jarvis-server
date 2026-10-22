package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// The kitchen is a restaurant ordering site shaped like the real one a computer task failed on
// (#485, a Toast menu): a long menu, so the page has more than the snapshot's 300 elements and a
// dialog comes after all of them; "Add X to Cart" buttons that only open the item's options
// dialog; a cart dialog with upsell "+ X" buttons next to the Remove buttons; and a cart that
// already holds someone's leftovers, which the task has to clear before checking out.
//
//	GET  /kitchen/                 the menu (?cart=open: with the cart dialog)
//	GET  /kitchen/item/<id>        the menu with the item's options dialog
//	POST /kitchen/cart/add         id, qty, opt (repeated) → the menu with the cart dialog
//	POST /kitchen/cart/remove      id → the menu with the cart dialog
//	GET  /kitchen/checkout         order details and Place Order
//	POST /kitchen/checkout         places the order → /kitchen/order/<number>

// Dish is one menu item.
type Dish struct {
	ID    string
	Name  string
	Price float64
}

// The kitchen's fixed answers for the suite: the dish the case orders, and the leftovers.
const KitchenDish = "pulled-pork-sandwich"

// kitchenCart is the cart key (the kitchen needs no login: a guest order).
const kitchenCart = "kitchen"

var kitchenOptions = []string{"No Cole Slaw", "No Pickles", "No BBQ Sauce", "Extra Pickles", "Add Pimento Cheese"}

var menu = buildMenu()

// buildMenu is 150 dishes: the named ones first, then sides and sizes, so the page is long.
func buildMenu() []Dish {
	named := []Dish{
		{"pulled-pork-sandwich", "Pulled Pork Sandwich", 14},
		{"smokehouse-burger", "Smokehouse Thick Burger", 19},
		{"brisket-sandwich", "Chopped Brisket Sandwich", 16},
		{"pulled-pork-half-pound", "Pulled Pork - Half Pound", 9},
		{"pulled-pork-pound", "Pulled Pork - Pound", 17},
		{"smoked-wings-6", "Smoked Wings (6)", 11},
		{"potato-salad-pint", "Texas Carolina Potato Salad - Pt.", 10},
		{"mac-and-cheese", "Smoked Mac and Cheese", 8},
		{"cornbread", "Honey Butter Cornbread", 5},
		{"banana-pudding", "Banana Pudding", 7},
	}
	sides := []string{"Cole Slaw", "Baked Beans", "Collard Greens", "Fries", "Hush Puppies", "Pickled Onions",
		"Burnt Ends", "Turkey Breast", "Sausage Link", "Rib Tips", "Hot Links", "Smoked Chicken",
		"Brisket Chili", "Corn on the Cob"}
	sizes := []string{"Small", "Medium", "Large", "Family", "Party Tray", "Half Pan", "Full Pan", "Catering Box",
		"Kids", "Add-On"}
	out := named
	for _, sz := range sizes {
		for i, sd := range sides {
			if len(out) == 150 {
				return out
			}
			id := strings.ToLower(strings.ReplaceAll(sz+"-"+sd, " ", "-"))
			out = append(out, Dish{id, sd + " - " + sz, float64(4 + (i+len(sz))%12)})
		}
	}
	return out
}

func dishByID(id string) (Dish, bool) {
	for _, d := range menu {
		if d.ID == id {
			return d, true
		}
	}
	return Dish{}, false
}

// leftovers is what the cart holds before the task: two of the dish and a burger.
func leftovers() []CartItem {
	return []CartItem{
		{ID: "pulled-pork-sandwich", Name: "Pulled Pork Sandwich", Price: 14, Qty: 2},
		{ID: "smokehouse-burger", Name: "Smokehouse Thick Burger", Price: 19, Qty: 1},
	}
}

// upsells are the cart dialog's "+ X" buttons.
var upsells = []string{"smokehouse-burger", "potato-salad-pint", "pulled-pork-half-pound"}

type kitchenData struct {
	Menu    []Dish
	Item    *Dish    // the open options dialog, or nil
	Options []string // the item dialog's options
	Cart    *cartData
	Upsells []Dish
	Count   int
	Total   float64
}

// kitchenCartItems is the cart, seeded with the leftovers the first time (and after a reset).
// The caller holds s.mu.
func (s *Server) kitchenCartItems() []CartItem {
	c, ok := s.carts[kitchenCart]
	if !ok {
		c = leftovers()
		s.carts[kitchenCart] = c
	}
	return c
}

func (s *Server) kitchenData(item *Dish, cartOpen bool) kitchenData {
	s.mu.Lock()
	items := append([]CartItem(nil), s.kitchenCartItems()...)
	s.mu.Unlock()
	d := kitchenData{Menu: menu, Item: item}
	if item != nil {
		d.Options = kitchenOptions
	}
	cd := cartData{Items: items}
	for _, it := range items {
		d.Count += it.Qty
		cd.Total += it.Price * float64(it.Qty)
	}
	cd.Total = cents(cd.Total)
	d.Total = cd.Total
	if cartOpen {
		d.Cart = &cd
		for _, id := range upsells {
			if u, ok := dishByID(id); ok {
				d.Upsells = append(d.Upsells, u)
			}
		}
	}
	return d
}

func (s *Server) kitchen(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/kitchen")
	switch {
	case path == "/" && r.Method == http.MethodGet:
		s.render(w, r, http.StatusOK, "kitchen", "Smokehouse Kitchen | Order online",
			s.kitchenData(nil, r.URL.Query().Get("cart") == "open"))
	case strings.HasPrefix(path, "/item/") && r.Method == http.MethodGet:
		d, ok := dishByID(strings.TrimPrefix(path, "/item/"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		s.render(w, r, http.StatusOK, "kitchen", d.Name+" | Smokehouse Kitchen", s.kitchenData(&d, false))
	case path == "/cart/add" && r.Method == http.MethodPost:
		d, ok := dishByID(r.FormValue("id"))
		if !ok {
			http.Error(w, "no such dish", http.StatusBadRequest)
			return
		}
		qty, err := strconv.Atoi(r.FormValue("qty"))
		if err != nil || qty < 1 {
			qty = 1
		}
		name := d.Name
		if opts := r.Form["opt"]; len(opts) > 0 {
			name += " (" + strings.Join(opts, ", ") + ")"
		}
		s.mu.Lock()
		cart := s.kitchenCartItems()
		found := false
		for i := range cart {
			if cart[i].ID == d.ID && cart[i].Name == name {
				cart[i].Qty += qty
				found = true
			}
		}
		if !found {
			cart = append(cart, CartItem{ID: d.ID, Name: name, Price: d.Price, Qty: qty})
		}
		s.carts[kitchenCart] = cart
		s.save()
		s.mu.Unlock()
		http.Redirect(w, r, "/kitchen/?cart=open", http.StatusSeeOther)
	case path == "/cart/remove" && r.Method == http.MethodPost:
		s.mu.Lock()
		var cart []CartItem
		for i, it := range s.kitchenCartItems() {
			if strconv.Itoa(i) != r.FormValue("line") {
				cart = append(cart, it)
			}
		}
		if cart == nil {
			cart = []CartItem{}
		}
		s.carts[kitchenCart] = cart
		s.save()
		s.mu.Unlock()
		back := "/kitchen/?cart=open"
		if r.FormValue("from") == "checkout" {
			back = "/kitchen/checkout"
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
	case path == "/checkout" && r.Method == http.MethodGet:
		d := s.kitchenData(nil, true)
		d.Menu = nil
		s.render(w, r, http.StatusOK, "kitchen_checkout", "Checkout | Smokehouse Kitchen", d)
	case path == "/checkout" && r.Method == http.MethodPost:
		s.mu.Lock()
		items := append([]CartItem(nil), s.kitchenCartItems()...)
		if len(items) == 0 {
			s.mu.Unlock()
			http.Redirect(w, r, "/kitchen/?cart=open", http.StatusSeeOther)
			return
		}
		total := 0.0
		for _, it := range items {
			total += it.Price * float64(it.Qty)
		}
		o := Order{Number: fmt.Sprintf("SK-%05d", 20001+len(s.st.Orders)), Time: s.now().UTC(), Items: items,
			Total: cents(total), ShipTo: Address{Name: "Pickup"}, Card: savedCard}
		s.st.Orders = append(s.st.Orders, o)
		s.carts[kitchenCart] = []CartItem{}
		s.save()
		s.mu.Unlock()
		http.Redirect(w, r, "/kitchen/order/"+o.Number, http.StatusSeeOther)
	case strings.HasPrefix(path, "/order/") && r.Method == http.MethodGet:
		num := strings.TrimPrefix(path, "/order/")
		s.mu.Lock()
		var o *Order
		for i := range s.st.Orders {
			if s.st.Orders[i].Number == num {
				o = &s.st.Orders[i]
			}
		}
		s.mu.Unlock()
		if o == nil {
			http.NotFound(w, r)
			return
		}
		s.render(w, r, http.StatusOK, "store_order", "Order "+num+" | Smokehouse Kitchen", *o)
	default:
		http.NotFound(w, r)
	}
}

package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// The menu site (/menu/) is a big restaurant ordering page in the shape of a Toast online-ordering
// page (#485): a header, a category bar, about 230 menu items each a link whose accessible name
// carries the item's description and price, and a long footer, so a snapshot is close to the
// agent's 300-element default and the page text runs past its cap. An item's page adds it to the
// guest cart (no login, like Toast's guest ordering). The pulled pork sandwich sits among
// look-alikes (platter, nachos, pulled chicken), mid-page.

// MenuItem is one dish.
type MenuItem struct {
	ID          string
	Name        string
	Price       float64
	Description string
	Category    string
}

// MenuCategory is one section of the menu.
type MenuCategory struct {
	ID    string
	Name  string
	Items []MenuItem
}

// PulledPorkSandwich is the item the suite's menu case adds to the cart.
const PulledPorkSandwich = "pulled-pork-sandwich"

var menu = buildMenu()

// menuSections lists every category's dishes, "Name|price|what's in it".
var menuSections = []struct{ name, items string }{
	{"Popular", "Brisket Plate|21.95|half pound of 14-hour post oak brisket, two sides, pickles, onions, white bread;Burnt Ends|14.50|caramelized point-end cubes tossed in house sauce;Smoked Wings (8)|13.95|dry-rubbed, smoked then fried, ranch or blue cheese;Mac & Cheese Skillet|9.50|three-cheese sauce, cavatappi, toasted panko;Banana Pudding|6.50|vanilla wafers, whipped cream, fresh banana;Rib Tips|12.95|St. Louis tips, sweet glaze, pickles;Smoked Turkey Club|14.25|turkey breast, bacon, lettuce, tomato, mayo on toasted sourdough;Loaded Baked Potato|11.50|butter, sour cream, cheddar, chives, choice of chopped meat"},
	{"Starters", "Fried Pickles|7.95|dill chips, cornmeal batter, comeback sauce;Brisket Queso|10.50|white queso, chopped brisket, pico, tortilla chips;Pulled Pork Nachos|12.95|tortilla chips, queso, pulled pork, jalapeños, pico, crema;Smoked Sausage Bites|9.95|jalapeño cheddar sausage, mustard, crackers;Hushpuppies|6.50|honey butter, sweet corn;Deviled Eggs|7.25|smoked paprika, candied bacon;Fried Green Tomatoes|8.95|remoulade, pimento cheese;Pork Rinds|5.50|chili lime salt;Burnt End Poutine|13.25|fries, gravy, cheese curds, burnt ends;Smoked Chicken Dip|9.75|crackers, celery, hot sauce;Onion Strings|6.95|buttermilk battered, chipotle mayo;Jalapeño Poppers|8.50|cream cheese, bacon wrapped, smoked"},
	{"Plates", "Pulled Pork Platter|17.95|half pound pulled pork shoulder, two sides, cornbread, pickles;Sliced Brisket Platter|22.95|half pound lean or moist brisket, two sides, cornbread;Half Chicken Platter|16.95|smoked half chicken, two sides, cornbread;Spare Rib Platter|24.95|half rack of St. Louis ribs, two sides, cornbread;Three Meat Platter|27.95|choose three meats, three sides, cornbread;Two Meat Platter|23.95|choose two meats, two sides, cornbread;Sausage Platter|15.95|two links jalapeño cheddar sausage, two sides;Turkey Platter|17.25|smoked turkey breast, two sides, cornbread;Pulled Chicken Platter|15.95|hand-pulled smoked chicken, two sides, cornbread;Burnt Ends Platter|21.50|half pound burnt ends, two sides, cornbread;Pork Belly Platter|19.95|glazed smoked pork belly, two sides, cornbread;Beef Rib Platter|39.95|one giant beef plate rib, two sides, cornbread"},
	{"Sandwiches", "Brisket Sandwich|14.95|chopped or sliced brisket, pickles, onions, brioche bun;Pulled Chicken Sandwich|12.50|hand-pulled smoked chicken, Alabama white sauce, slaw, brioche bun;Pulled Pork Sandwich|12.95|smoked pork shoulder, house sauce, slaw, pickles, brioche bun;Hot Link Sandwich|11.95|split hot link, mustard, onions, hoagie roll;Turkey Sandwich|12.95|smoked turkey, provolone, lettuce, tomato, mayo, sourdough;Cuban|13.95|pulled pork, ham, swiss, pickles, mustard, pressed Cuban bread;Burnt End Melt|14.50|burnt ends, pepper jack, grilled onions, Texas toast;Smash Burger|13.50|two patties, American cheese, pickles, onions, burger sauce;Fried Chicken Sandwich|12.95|buttermilk thigh, hot honey, pickles, slaw;Pork Belly BLT|14.25|smoked pork belly, lettuce, tomato, garlic aioli;Veggie Burger|12.25|black bean patty, avocado, pickled onion;Brisket Grilled Cheese|13.95|chopped brisket, cheddar, American, Texas toast;Sausage Po'boy|12.50|smoked sausage, remoulade, lettuce, tomato;Pulled Pork Slider Trio|11.95|three sliders, pulled pork, slaw, pickles"},
	{"Tacos & Bowls", "Brisket Tacos (3)|14.50|corn tortillas, chopped brisket, onion, cilantro, salsa verde;Pulled Pork Tacos (3)|12.95|flour tortillas, pulled pork, pineapple salsa, slaw;Chicken Tacos (3)|12.50|smoked chicken, pico, crema, cotija;Burnt End Tacos (3)|14.95|burnt ends, pickled onion, jalapeño;Brisket Bowl|15.50|rice, beans, brisket, corn, pico, queso;Pulled Pork Bowl|13.95|rice, beans, pulled pork, slaw, pickles;Chicken Bowl|13.50|rice, beans, chicken, corn, pico;Veggie Bowl|12.25|rice, beans, roasted vegetables, avocado;Sausage Bowl|12.95|dirty rice, sausage, peppers, onions;Fish Tacos (3)|13.95|fried catfish, slaw, lime crema;Taco Salad|12.50|lettuce, choice of meat, beans, cheese, chips;Street Corn Bowl|10.95|charred corn, cotija, lime, chili"},
	{"By the Pound", "Moist Brisket (1 lb)|29.00|fatty point-end brisket;Lean Brisket (1 lb)|29.00|flat-end brisket;Pulled Pork (1 lb)|18.00|smoked pork shoulder, sauce on the side;Pulled Chicken (1 lb)|17.00|smoked chicken, sauce on the side;Smoked Turkey (1 lb)|20.00|turkey breast;Burnt Ends (1 lb)|26.00|sauced point-end cubes;Pork Belly (1 lb)|22.00|glazed;Jalapeño Cheddar Sausage (link)|5.50|one link;Original Sausage (link)|5.00|one link;Hot Link (link)|5.25|one link;Spare Ribs (half rack)|19.00|St. Louis cut;Spare Ribs (full rack)|35.00|St. Louis cut;Beef Rib (each)|34.00|plate rib, about 1.5 lb;Half Chicken|11.00|smoked"},
	{"Family Packs", "Family Pack for 4|69.95|two pounds of meat, three pint sides, eight rolls, sauce;Family Pack for 6|99.95|three pounds of meat, four pint sides, twelve rolls, sauce;Pulled Pork Party Pack|79.95|three pounds pulled pork, buns, slaw, pickles, sauce;Brisket Party Pack|119.95|three pounds brisket, buns, onions, pickles, sauce;Rib Feast|89.95|three racks, three pint sides;Wing Bucket (24)|36.95|three flavors, ranch and blue cheese;Taco Kit for 6|59.95|meat, tortillas, toppings, salsa;Slider Kit (12)|49.95|choice of meat, slaw, pickles, buns;Holiday Turkey|74.95|whole smoked turkey, about 12 lb;Whole Brisket|159.00|whole packer, about 12 lb cooked;Sides Trio (quarts)|29.95|three quart sides;Dessert Pack|24.95|banana pudding, cobbler, cookies for 6"},
	{"Salads", "Smokehouse Cobb|13.95|greens, turkey, bacon, egg, avocado, blue cheese;Brisket Salad|14.95|greens, brisket, crispy onions, tomato, ranch;Pulled Chicken Salad|12.95|greens, pulled chicken, corn, black beans, chipotle ranch;House Salad|7.95|greens, tomato, cucumber, croutons;Caesar|8.95|romaine, parmesan, croutons, Caesar dressing;Wedge|9.50|iceberg, blue cheese, bacon, tomato;Southwest Salad|11.95|greens, corn, beans, pico, tortilla strips;Kale Slaw Salad|9.25|kale, cabbage, apple, pecans;Pork Belly Salad|13.50|greens, pork belly, peaches, pecans, vinaigrette;Side Salad|4.95|greens, tomato, cucumber"},
	{"Sides", "Coleslaw|3.95|creamy;Vinegar Slaw|3.95|tangy;Potato Salad|3.95|mustard style;Baked Beans|3.95|with burnt ends;Collard Greens|4.25|smoked turkey;Mac & Cheese|4.50|three cheese;Cornbread|2.95|honey butter;Fries|3.95|seasoned;Sweet Potato Fries|4.25|cinnamon sugar;Green Beans|3.95|bacon, onion;Corn on the Cob|3.50|butter, chili salt;Pickles & Onions|1.50|house pickles;Texas Toast|1.95|garlic butter;Dirty Rice|3.95|sausage, peppers;Fried Okra|4.25|cornmeal;Creamed Corn|3.95|jalapeño"},
	{"Kids", "Kids Pulled Pork Slider|6.95|one slider, fries, drink;Kids Chicken Tenders|7.50|two tenders, fries, drink;Kids Mac & Cheese|5.95|mac, apple slices, drink;Kids Grilled Cheese|5.95|American cheese, fries, drink;Kids Brisket Plate|8.95|brisket, one side, drink;Kids Hot Dog|5.50|all-beef hot dog, fries, drink;Kids Corn Dog|5.95|fries, drink;Kids Quesadilla|6.25|cheese, chicken, salsa, drink"},
	{"Desserts", "Peach Cobbler|6.95|vanilla ice cream;Pecan Pie|6.50|whipped cream;Chocolate Chess Pie|6.50|whipped cream;Bourbon Bread Pudding|7.25|caramel;Key Lime Pie|6.50|graham crust;Cookie Skillet|8.95|chocolate chip, ice cream;Fried Pie|4.95|apple or peach;Brownie|4.50|walnut;Banana Pudding Jar|6.50|to go;Soft Serve|3.95|vanilla, chocolate or swirl;Sweet Potato Pie|6.25|whipped cream;Lemon Bar|3.95|powdered sugar"},
	{"Drinks", "Sweet Tea|2.95|fresh brewed;Unsweet Tea|2.95|fresh brewed;Lemonade|3.50|fresh squeezed;Arnold Palmer|3.50|tea and lemonade;Coke|2.75|can;Diet Coke|2.75|can;Sprite|2.75|can;Dr Pepper|2.75|can;Root Beer|3.25|bottle;Bottled Water|1.95|16 oz;Sparkling Water|2.50|can;Cold Brew|4.25|16 oz;Gallon Sweet Tea|9.95|serves 8;Gallon Lemonade|11.95|serves 8"},
	{"Sauces & Extras", "House BBQ Sauce|0.75|2 oz;Spicy BBQ Sauce|0.75|2 oz;Carolina Mustard Sauce|0.75|2 oz;Alabama White Sauce|0.75|2 oz;Vinegar Pepper Sauce|0.75|2 oz;Ranch|0.75|2 oz;Blue Cheese|0.75|2 oz;Pint of House Sauce|6.95|to go;Pickled Jalapeños|1.25|side;Extra Bun|1.00|brioche;Cheese Sauce|1.95|side;Utensils & Napkins|0.00|for the table"},
	{"Merch", "Smoke & Oak T-Shirt|24.00|black, S to XXL;Trucker Hat|22.00|mesh back;Rub Jar|12.00|8 oz house rub;Sauce Gift Set|28.00|three bottles;Gift Card $25|25.00|delivered by email;Gift Card $50|50.00|delivered by email;Koozie|5.00|neoprene;Apron|30.00|canvas, leather straps;Enamel Pin|8.00|pitmaster pig;Sticker Pack|6.00|five stickers"},
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(slugRE.ReplaceAllString(strings.ToLower(strings.ReplaceAll(s, "&", "and")), "-"), "-")
}

func buildMenu() []MenuCategory {
	cats := make([]MenuCategory, 0, len(menuSections))
	for _, sec := range menuSections {
		c := MenuCategory{ID: slug(sec.name), Name: sec.name}
		for _, it := range strings.Split(sec.items, ";") {
			f := strings.SplitN(it, "|", 3)
			price, err := strconv.ParseFloat(f[1], 64)
			if err != nil {
				panic("menu: " + it)
			}
			id := slug(f[0])
			if c.ID == "popular" {
				id = "popular-" + id // the Popular row repeats dishes under their own ids
			}
			desc := fmt.Sprintf("%s. %s", strings.ToUpper(f[2][:1])+f[2][1:], menuBlurb(len(c.Items)))
			c.Items = append(c.Items, MenuItem{ID: id, Name: f[0], Price: price, Description: desc, Category: sec.name})
		}
		cats = append(cats, c)
	}
	return cats
}

// menuBlurb is the marketing copy Toast menus carry under each dish, so the page text is long.
func menuBlurb(i int) string {
	blurbs := []string{
		"Smoked low and slow over post oak for up to 14 hours in our offset pits, every single day.",
		"Contains gluten. Ask about allergens; our kitchen handles nuts, dairy, soy and wheat.",
		"A guest favorite since we opened the Springfield pit in 2014. Limited each day: when it's gone, it's gone.",
		"Made from scratch every morning with locally sourced produce from Orchard Lane Farms.",
	}
	return blurbs[i%len(blurbs)]
}

func menuItemByID(id string) (MenuItem, bool) {
	for _, c := range menu {
		for _, it := range c.Items {
			if it.ID == id {
				return it, true
			}
		}
	}
	return MenuItem{}, false
}

type menuData struct {
	Categories []MenuCategory
	Cart       []CartItem
	Count      int
	Total      float64
	Added      string // the item just added, for the banner
	Item       MenuItem
	Error      string
}

func (s *Server) menuData() menuData {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := menuData{Categories: menu, Cart: append([]CartItem(nil), s.st.MenuCart...)}
	for _, it := range d.Cart {
		d.Count += it.Qty
		d.Total = cents(d.Total + it.Price*float64(it.Qty))
	}
	return d
}

// menuSite serves /menu/: the menu, an item's page, adding to the cart and the cart.
func (s *Server) menuSite(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/menu")
	switch {
	case path == "/" && r.Method == http.MethodGet:
		d := s.menuData()
		if it, ok := menuItemByID(r.URL.Query().Get("added")); ok {
			d.Added = it.Name
		}
		s.render(w, r, http.StatusOK, "menu", "Smoke & Oak BBQ - Springfield | Order Online", d)
	case strings.HasPrefix(path, "/item/") && r.Method == http.MethodGet:
		it, ok := menuItemByID(strings.TrimPrefix(path, "/item/"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		d := s.menuData()
		d.Item = it
		s.render(w, r, http.StatusOK, "menu_item", it.Name+" | Smoke & Oak BBQ", d)
	case path == "/cart/add" && r.Method == http.MethodPost:
		it, ok := menuItemByID(r.FormValue("id"))
		if !ok {
			http.Error(w, "no such item", http.StatusBadRequest)
			return
		}
		qty, err := strconv.Atoi(r.FormValue("qty"))
		if err != nil || qty < 1 || qty > 20 {
			qty = 1
		}
		s.mu.Lock()
		added := false
		for i := range s.st.MenuCart {
			if s.st.MenuCart[i].ID == it.ID {
				s.st.MenuCart[i].Qty += qty
				added = true
			}
		}
		if !added {
			s.st.MenuCart = append(s.st.MenuCart, CartItem{ID: it.ID, Name: it.Name, Price: it.Price, Qty: qty})
		}
		s.save()
		s.mu.Unlock()
		http.Redirect(w, r, "/menu/?added="+it.ID, http.StatusSeeOther)
	case path == "/cart/remove" && r.Method == http.MethodPost:
		id := r.FormValue("id")
		s.mu.Lock()
		cart := s.st.MenuCart[:0]
		for _, it := range s.st.MenuCart {
			if it.ID != id {
				cart = append(cart, it)
			}
		}
		s.st.MenuCart = cart
		s.save()
		s.mu.Unlock()
		http.Redirect(w, r, "/menu/cart", http.StatusSeeOther)
	case path == "/cart" && r.Method == http.MethodGet:
		s.render(w, r, http.StatusOK, "menu_cart", "Your order | Smoke & Oak BBQ", s.menuData())
	default:
		http.NotFound(w, r)
	}
}

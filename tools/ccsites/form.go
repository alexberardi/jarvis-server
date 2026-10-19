package main

import (
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The form: a community garden plot application with ten fields.
var (
	formFields     = []string{"full_name", "email", "phone", "start_date", "country", "plan", "contact_method", "newsletter", "terms", "message"}
	countries      = []string{"US", "CA", "GB", "DE", "FR"}
	plans          = []string{"basic", "standard", "premium"}
	contactMethods = []string{"email", "phone", "text"}
	phoneRE        = regexp.MustCompile(`^[0-9 ()+.-]{7,20}$`)
)

type formData struct {
	Values         map[string]string
	Errors         map[string]string
	Countries      []string
	Plans          []string
	ContactMethods []string
	ID             string
}

func (s *Server) form(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/form")
	switch {
	case path == "/" && r.Method == http.MethodGet:
		s.render(w, r, http.StatusOK, "form", "Garden plot application", s.formData(nil, nil))
	case path == "/" && r.Method == http.MethodPost:
		vals := map[string]string{}
		for _, f := range formFields {
			vals[f] = strings.TrimSpace(r.PostFormValue(f))
		}
		if errs := validate(vals); len(errs) > 0 {
			s.render(w, r, http.StatusUnprocessableEntity, "form", "Garden plot application", s.formData(vals, errs))
			return
		}
		s.mu.Lock()
		id := fmt.Sprintf("GP-%04d", 1+len(s.st.Forms))
		s.st.Forms = append(s.st.Forms, FormSub{ID: id, Time: s.now().UTC(), Fields: vals})
		s.save()
		s.mu.Unlock()
		http.Redirect(w, r, "/form/success?id="+id, http.StatusSeeOther)
	case path == "/success" && r.Method == http.MethodGet:
		s.render(w, r, http.StatusOK, "form_success", "Application received", formData{ID: r.URL.Query().Get("id")})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) formData(vals, errs map[string]string) formData {
	if vals == nil {
		vals = map[string]string{}
	}
	return formData{Values: vals, Errors: errs, Countries: countries, Plans: plans, ContactMethods: contactMethods}
}

// validate returns a message per bad field; newsletter is the only optional one.
func validate(v map[string]string) map[string]string {
	errs := map[string]string{}
	need := func(f, label string) bool {
		if v[f] == "" {
			errs[f] = label + " is required."
			return false
		}
		return true
	}
	need("full_name", "Full name")
	if need("email", "Email") {
		if a, err := mail.ParseAddress(v["email"]); err != nil || a.Address != v["email"] || !strings.Contains(v["email"], ".") {
			errs["email"] = "Enter a valid email address."
		}
	}
	if need("phone", "Phone") && !phoneRE.MatchString(v["phone"]) {
		errs["phone"] = "Enter a valid phone number."
	}
	if need("start_date", "Start date") {
		if _, err := time.Parse("2006-01-02", v["start_date"]); err != nil {
			errs["start_date"] = "Enter a date as YYYY-MM-DD."
		}
	}
	if need("country", "Country") && !slices.Contains(countries, v["country"]) {
		errs["country"] = "Choose a country from the list."
	}
	if need("plan", "Plot size") && !slices.Contains(plans, v["plan"]) {
		errs["plan"] = "Choose a plot size."
	}
	if need("contact_method", "Contact method") && !slices.Contains(contactMethods, v["contact_method"]) {
		errs["contact_method"] = "Choose a contact method."
	}
	if v["newsletter"] != "" && v["newsletter"] != "on" {
		errs["newsletter"] = "Invalid value."
	}
	if v["terms"] != "on" {
		errs["terms"] = "You must accept the garden rules."
	}
	need("message", "Message")
	return errs
}

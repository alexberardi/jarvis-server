package config

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
)

func TestServicesAddRemoveProbe(t *testing.T) {
	m, _ := setup(t, pconfig.ListenerConfig, pconfig.ListenerAuth)
	ctx := context.Background()

	// An external service with a health route.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer up.Close()

	e, err := m.AddService(ctx, NewService{Name: "jarvis-web", URL: up.URL + "/", HealthPath: "/healthz", Description: " web chat "})
	if err != nil || e.URL != up.URL || e.HealthPath != "/healthz" || e.Description != "web chat" || e.Managed != "" {
		t.Fatalf("add: %+v %v", e, err)
	}
	if _, err := m.AddService(ctx, NewService{Name: "jarvis-web", URL: up.URL}); !errors.Is(err, ErrServiceExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := m.AddService(ctx, NewService{Name: "jarvis-auth", URL: "http://elsewhere:1"}); !errors.Is(err, ErrServiceManaged) {
		t.Fatalf("managed name: %v", err)
	}
	for _, bad := range []NewService{
		{Name: "x"},
		{Name: "x", URL: "not a url"},
		{Name: "x", URL: "http://h:1/path"},
		{Name: "x", URL: "http://user@h:1"},
		{Name: "x", URL: "ftp://h:1"},
		{Name: "", URL: "http://h:1"},
		{Name: "x", URL: "http://h:1", HealthPath: "health"},
	} {
		var ve *ValidationError
		if _, err := m.AddService(ctx, bad); !errors.As(err, &ve) || len(ve.Fields) == 0 {
			t.Fatalf("%+v accepted: %v", bad, err)
		}
	}
	// Default ports and health path.
	if e, err := m.AddService(ctx, NewService{Name: "satellite", URL: "https://gpu.lan"}); err != nil || e.Port != 443 || e.HealthPath != "/health" {
		t.Fatalf("defaults: %+v %v", e, err)
	}

	list, err := m.Services(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ServiceEntry{}
	for _, s := range list {
		byName[s.Name] = s
	}
	if byName["jarvis-auth"].Managed != ManagedListener || byName["jarvis-web"].Managed != "" || len(byName) != 4 {
		t.Fatalf("list: %+v", list)
	}

	h := m.ProbeAll(ctx, []ServiceEntry{byName["jarvis-web"]})
	if !h["jarvis-web"].Healthy {
		t.Fatalf("probe: %+v", h)
	}

	// A non-HTTP row is probed with a TCP connect.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if st := m.Probe(ctx, ServiceEntry{Name: "broker", Scheme: "mqtt", Host: "127.0.0.1", Port: port}); !st.Healthy {
		t.Fatalf("tcp probe: %+v", st)
	}
	ln.Close()
	if st := m.Probe(ctx, ServiceEntry{Name: "broker", Scheme: "mqtt", Host: "127.0.0.1", Port: port}); st.Healthy || st.Error == nil {
		t.Fatalf("closed port: %+v", st)
	}

	if err := m.RemoveService(ctx, "jarvis-auth"); !errors.Is(err, ErrServiceManaged) {
		t.Fatalf("remove managed: %v", err)
	}
	if err := m.RemoveService(ctx, "jarvis-web"); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveService(ctx, "jarvis-web"); !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
}

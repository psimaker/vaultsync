package syncthing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseGUIConfig(t *testing.T) {
	gui, err := ParseGUIConfig(strings.NewReader(`<configuration version="52">
    <gui enabled="true" tls="true"><address>127.0.0.1:41234</address><apikey>k3y</apikey></gui>
</configuration>`))
	if err != nil || gui.Address != "127.0.0.1:41234" || gui.APIKey != "k3y" || gui.TLS != "true" {
		t.Fatalf("%+v %v", gui, err)
	}
	if _, err := ParseGUIConfig(strings.NewReader("<configuration><gui>")); err == nil {
		t.Fatal("a truncated config must not parse")
	}
}

func TestClientSendsTheKeyAndNeverFollowsRedirects(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the API key followed a redirect: %s", r.Header.Get("X-API-Key"))
	}))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "k" {
			http.Error(w, "Not Authorized", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/rest/cluster/pending/folders":
			_, _ = w.Write([]byte(`{"vs-1":{"offeredBy":{"HUB":{"time":"2026-10-06T10:00:00Z","label":"Notes"}}}}`))
		case "/rest/system/ping":
			http.Redirect(w, r, elsewhere.URL+"/rest/system/ping", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	offers, err := NewClient(srv.URL, "k").PendingOffers(ctx)
	if err != nil || offers["vs-1"]["HUB"].Label != "Notes" {
		t.Fatalf("%+v %v", offers, err)
	}
	pending, err := NewClient(srv.URL, "k").PendingFolders(ctx)
	if err != nil || len(pending["vs-1"]) != 1 || pending["vs-1"][0] != "HUB" {
		t.Fatalf("%+v %v", pending, err)
	}
	if err := NewClient(srv.URL, "k").Ping(ctx); err == nil {
		t.Fatal("a redirect must fail")
	}
	var apiErr *APIError
	if _, err := NewClient(srv.URL, "wrong").Folders(ctx); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("expected a 403 APIError, got %v", err)
	}
}

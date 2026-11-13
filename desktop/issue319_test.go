package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/psimaker/vaultsync/hub/pairing"
)

// #319: the text of a Hub's error answer comes from whatever answered at the
// address that was dialled, and `vaultsync pair` shows it — in a terminal,
// and through the menu in a dialog. Decision 062.

// hidden reports a character that is not shown as written: a control
// character, a format character, a line or paragraph separator.
func hidden(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

// A failure the agent has no words of its own for ended with the answer's
// text as it came: a terminal sequence, a mark that reverses what follows
// and line breaks, as long as an answer may be. It now ends with one clean,
// short line of it.
func TestIssue319_AHubsErrorAnswerIsShownAsOneCleanShortLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(pairing.ErrorResponse{Error: "\x1b[2Jhub state\u2028unavailable\u202e\r\n$ " + strings.Repeat("я", 30000)})
	}))
	defer srv.Close()
	eng := newFakeEngine(t)
	s, out := testSession(t, eng, nil, pairOptions{hub: strings.TrimPrefix(srv.URL, "http://"), code: "TULIP-ANCHOR-42"})
	msg := refusalText(s.run(context.Background()))
	const lead = "Pairing with your Hub failed: hub pairing: HTTP 500: "
	if !strings.HasPrefix(msg, lead+"[2Jhub state unavailable $ яя") || !strings.HasSuffix(msg, "я…") {
		t.Errorf("the refusal: %+q\n%s", msg, out)
	}
	if len(msg) > len(lead)+300 || strings.ContainsFunc(msg, hidden) || strings.ContainsFunc(strings.ReplaceAll(out.String(), "\n", ""), hidden) {
		t.Errorf("the refusal is %d bytes behind the agent's own words, or something is shown that is not written: %+q\n%+q", len(msg)-len(lead), msg, out)
	}
}

// A Hub that knows the code says inside its encrypted answer why it shared
// nothing — what its Syncthing answered, for one — and the agent shows that
// text too. It is cleaned and cut the same way, and a refused registration
// is still told by its first words.
func TestIssue319_TheErrorInAHubsReplyIsShownAsOneCleanShortLine(t *testing.T) {
	const dirt = "\r\n\x1b[2J\u202esyncthing API: HTTP 500:\u2028"
	// As long as an encrypted answer can be: its box has to fit in 64 KiB.
	tail := strings.Repeat("я", 10000)
	cases := []struct {
		name, reported, own, kept string
	}{
		{"the Hub could not share the vault",
			"could not share the vault:" + dirt + tail,
			"Your Hub could not share “Recipes”: ",
			"could not share the vault: [2Jsyncthing API: HTTP 500: яя"},
		{"the Hub could not register this computer",
			pairing.RegistrationRefusedPrefix + ":" + dirt + tail,
			"Your Hub could not register this computer: ",
			"[2Jsyncthing API: HTTP 500: яя"},
	}
	for _, c := range cases {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Recipes", Files: 12})
		hub.noOffer = true
		hub.tamper = func(p *pairing.HubPayload) { p.Provisioned, p.Error = nil, c.reported }
		s, out := testSession(t, eng, hub, pairOptions{vault: "Recipes"})
		s.opts.code = hub.code
		s.opts.path = filepath.Join(s.env.home, "Vaults", "Recipes")
		msg := refusalText(s.run(context.Background()))
		const end = ". Check the Hub with “vaultsync-hub status”, then try again."
		if !strings.HasPrefix(msg, c.own+c.kept) || !strings.HasSuffix(msg, "я…"+end) {
			t.Errorf("%s: the refusal: %+q\n%s", c.name, msg, out)
		}
		if len(msg) > len(c.own)+300+len(end) || strings.ContainsFunc(msg, hidden) {
			t.Errorf("%s: the Hub's text is %d bytes of the refusal, or shows what is not written: %+q", c.name, len(msg)-len(c.own)-len(end), msg)
		}
		if eng.folderCount() != 0 {
			t.Errorf("%s: a folder was added", c.name)
		}
	}
}

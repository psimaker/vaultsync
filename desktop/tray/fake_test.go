package tray

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/layout"
)

// shortDir is a temporary directory short enough for a socket address
// (104 bytes on macOS); t.TempDir is not.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vst")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeAgent is the agent's side as the menu-bar app sees it: a socket in
// a VaultSync folder of its own that answers status, pause, resume and
// pair with what the test put there.
type fakeAgent struct {
	t        *testing.T
	stateDir string
	layout   layout.Layout // rooted at stateDir, no log folder
	logs     string        // a log folder with a vaultsync.log, for a Mac-like controller
	socket   string
	srv      *http.Server

	mu     sync.Mutex
	status Status
	// statusErr: status answers with this error (500).
	statusErr string
	hubs      int
	calls     []string
	pairs     []PairRequest
	// pair scripts the answer to each pair request.
	pair func(PairRequest) (PairResult, int)
	// old: an agent without this API (404 on everything).
	old bool
	// statusHold, when set, keeps every status request waiting until it
	// is closed.
	statusHold chan struct{}
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	f := &fakeAgent{t: t, stateDir: shortDir(t), logs: filepath.Join(t.TempDir(), "Logs")}
	if err := os.MkdirAll(f.logs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.logs, "vaultsync.log"), []byte("log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.socket = filepath.Join(f.stateDir, "agent.sock")
	f.status = Status{Version: "9.9.9", Syncthing: "2.1.6", Engine: "running", Report: Report{Hubs: []Hub{}, Vaults: []Vault{}, Waiting: []Waiting{}, Attempts: []Attempt{}}}
	f.writeState("")
	f.layout = layout.Layout{}.At(f.stateDir)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		hold := f.statusHold
		f.mu.Unlock()
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.old {
			http.NotFound(w, r)
			return
		}
		if f.statusErr != "" {
			writeJSON(w, 500, map[string]string{"error": f.statusErr})
			return
		}
		writeJSON(w, 200, f.status)
	})
	mux.HandleFunc("POST /v1/{verb}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		verb := r.PathValue("verb")
		f.calls = append(f.calls, verb)
		switch verb {
		case "pause", "resume":
			if f.hubs > 0 {
				f.status.Paused = verb == "pause"
			}
			writeJSON(w, 200, PauseResult{Paused: verb == "pause", Hubs: f.hubs})
		case "pair":
			var req PairRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
			f.pairs = append(f.pairs, req)
			if f.pair == nil {
				writeJSON(w, 500, map[string]string{"error": "no pairing scripted"})
				return
			}
			res, code := f.pair(req)
			if code >= 400 {
				writeJSON(w, code, map[string]string{"error": res.Refusal})
				return
			}
			writeJSON(w, code, res)
		default:
			http.NotFound(w, r)
		}
	})
	l, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	f.srv = &http.Server{Handler: mux}
	go func() { _ = f.srv.Serve(l) }()
	t.Cleanup(func() { _ = f.srv.Close() })
	return f
}

// writeState writes the agent.json the menu-bar app reads; socket is the
// recorded fallback place, "" the usual one.
func (f *fakeAgent) writeState(socket string) {
	f.t.Helper()
	st := map[string]any{"version": 1, "guiPort": 8384}
	if socket != "" {
		st["controlSocket"] = socket
	}
	data, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(f.stateDir, "agent.json"), data, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeAgent) set(fn func(*fakeAgent)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeAgent) stop() { _ = f.srv.Close() }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// fakeUI records every view and lets the test click.
type fakeUI struct {
	t      *testing.T
	events chan Event
	views  chan View
}

func newFakeUI(t *testing.T) *fakeUI {
	return &fakeUI{t: t, events: make(chan Event), views: make(chan View, 64)}
}

func (u *fakeUI) Render(v View)        { u.views <- v }
func (u *fakeUI) Events() <-chan Event { return u.events }

// send hands an event to the controller, or fails the test when nobody
// takes it — a controller that ended early must not hang the test.
func (u *fakeUI) send(ev Event) {
	u.t.Helper()
	select {
	case u.events <- ev:
	case <-time.After(10 * time.Second):
		u.t.Fatalf("nobody took the event %+v: did the controller end?", ev)
	}
}

func (u *fakeUI) click(k EventKind, i int) { u.send(Event{Kind: k, Index: i}) }

// view waits for the next view that satisfies want.
func (u *fakeUI) view(t *testing.T, what string, want func(View) bool) View {
	t.Helper()
	deadline := time.After(10 * time.Second)
	var last View
	for {
		select {
		case v := <-u.views:
			last = v
			if want(v) {
				return v
			}
		case <-deadline:
			t.Fatalf("no view %s; last: %+v", what, last)
		}
	}
}

// fakeAsker answers the dialogs from a script and records what was asked.
type fakeAsker struct {
	mu      sync.Mutex
	code    string
	cancel  string // which dialog the person cancels at: code, hub, vault, consent
	hub     string
	choose  func(Menu) (Choice, bool)
	agree   bool
	asked   []string
	menus   []Menu
	texts   []string // consent texts
	told    []string // Inform
	release chan struct{}
}

func (a *fakeAsker) record(s string) {
	a.mu.Lock()
	a.asked = append(a.asked, s)
	a.mu.Unlock()
}

func (a *fakeAsker) wait() {
	if a.release != nil {
		<-a.release
	}
}

func (a *fakeAsker) AskCode(context.Context) (string, bool, error) {
	a.record("code")
	a.wait()
	return a.code, a.cancel != "code", nil
}

func (a *fakeAsker) AskHub(_ context.Context, refusal string) (string, bool, error) {
	a.record("hub:" + refusal)
	return a.hub, a.cancel != "hub", nil
}

func (a *fakeAsker) ChooseVault(_ context.Context, m Menu) (Choice, bool, error) {
	a.record("vault")
	a.mu.Lock()
	a.menus = append(a.menus, m)
	a.mu.Unlock()
	if a.cancel == "vault" {
		return Choice{}, false, nil
	}
	if a.choose == nil {
		return Choice{}, false, errors.New("no choice scripted")
	}
	c, ok := a.choose(m)
	return c, ok, nil
}

func (a *fakeAsker) Confirm(_ context.Context, text string) (bool, error) {
	a.record("consent")
	a.mu.Lock()
	a.texts = append(a.texts, text)
	a.mu.Unlock()
	return a.agree && a.cancel != "consent", nil
}

func (a *fakeAsker) Inform(_ context.Context, text string) error {
	a.record("inform")
	a.mu.Lock()
	a.told = append(a.told, text)
	a.mu.Unlock()
	return nil
}

func (a *fakeAsker) sequence() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.asked...)
}

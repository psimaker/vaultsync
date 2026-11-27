package bridge

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	_ "unsafe" // for go:linkname

	natpmp "github.com/jackpal/go-nat-pmp"
	"github.com/syncthing/syncthing/lib/nat"
	_ "github.com/syncthing/syncthing/lib/pmp" // defines the methods linked below
)

// Syncthing's NAT service maps the sync port through lib/pmp, whose
// AddPortMapping and GetExternalIPv4Address wrap the blocking NAT-PMP request
// in svcutil.CallWithContext: on a stop the call returns at once and the
// request finishes later on its own goroutine. Upstream read the request's
// result after the call whether it had completed or not, while the request
// could still assign it (#195, decision 043). The bridge turns NAT on, so on
// iOS the NAT service asks the network's gateway for a mapping after every
// start, and a stop during that request is the trigger.
//
// The test holds a request in flight at a NAT-PMP gateway it runs on
// loopback, stops the call, waits until the call has returned, and only then
// lets the gateway answer. The race detector is the oracle, so run it with
// -race; without it the test still checks that a stop does not wait for the
// gateway.
func TestIssue195_StopDuringNATPMPRequestIsRaceFree(t *testing.T) {
	requirePMPDeviceMirror(t)

	for _, c := range []struct {
		name string
		call func(*pmpDevice, context.Context) error
	}{
		{"AddPortMapping", func(d *pmpDevice, ctx context.Context) error {
			port, err := pmpAddPortMapping(d, ctx, nat.TCP, 22000, 22000, "", time.Hour)
			if port != 0 {
				return errors.New("a stopped mapping request returned a port")
			}
			return err
		}},
		{"GetExternalIPv4Address", func(d *pmpDevice, ctx context.Context) error {
			ip, err := pmpGetExternalIPv4Address(d, ctx)
			if !ip.Equal(net.IPv4zero) {
				return errors.New("a stopped address request returned an address")
			}
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			gateway := startHeldNATPMPGateway(t)
			device := newPMPDevice(gateway.ip)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			callerGoroutine := make(chan string, 1)
			returned := make(chan error, 1)
			go func() {
				callerGoroutine <- goroutineID()
				returned <- c.call(device, ctx)
			}()
			caller := <-callerGoroutine

			select {
			case <-gateway.requested:
			case <-time.After(10 * time.Second):
				t.Fatal("no NAT-PMP request reached the gateway")
			}
			request := goroutineCreatedBy("github.com/syncthing/syncthing/lib/svcutil.CallWithContext", caller, 10*time.Second)
			if request == "" {
				t.Fatal("found no goroutine running the NAT-PMP request")
			}

			cancel()
			// Wait for the call by watching the goroutine dump, not by
			// receiving from returned: a channel receive would order the
			// call's read before the test's release, and the race detector
			// would no longer see the late write this test exists to catch.
			if !awaitGoroutineExit(caller, 10*time.Second) {
				t.Fatal("the call did not return after the stop while the request was in flight")
			}

			gateway.release()
			if !awaitGoroutineExit(request, 10*time.Second) {
				t.Fatal("the request did not finish after the gateway answered")
			}

			if err := <-returned; !errors.Is(err, context.Canceled) {
				t.Fatalf("%s() = %v, want context.Canceled", c.name, err)
			}
		})
	}
}

// An answered request still hands the gateway's answer to the caller: reading
// the result only after a completed call must not lose it.
func TestIssue195_AnsweredNATPMPRequestReturnsTheAnswer(t *testing.T) {
	requirePMPDeviceMirror(t)

	gateway := startHeldNATPMPGateway(t)
	gateway.release()
	device := newPMPDevice(gateway.ip)

	port, err := pmpAddPortMapping(device, context.Background(), nat.TCP, 22000, 22000, "", time.Hour)
	if err != nil || port != natpmpTestMappedPort {
		t.Fatalf("AddPortMapping() = %d, %v; want %d, nil", port, err, natpmpTestMappedPort)
	}
	ip, err := pmpGetExternalIPv4Address(device, context.Background())
	if err != nil || !ip.Equal(natpmpTestExternalIP) {
		t.Fatalf("GetExternalIPv4Address() = %v, %v; want %v, nil", ip, err, natpmpTestExternalIP)
	}
}

// lib/pmp's device type is unexported, and pmp.Discover builds one only for
// the machine's default gateway, which no test controls. So the test builds
// the device itself: pmpDevice mirrors the type's fields, and its methods are
// called by their linker names. requirePMPDeviceMirror holds both against the
// engine's source before any call, comparing every type by its import path,
// so a Syncthing bump that changes them fails the test instead of running a
// method on the wrong memory; a method the engine no longer defines on
// *wrapper fails the build at link time.
type pmpDevice struct {
	renewal   time.Duration
	localIP   net.IP
	gatewayIP net.IP
	client    *natpmp.Client
}

//go:linkname pmpAddPortMapping github.com/syncthing/syncthing/lib/pmp.(*wrapper).AddPortMapping
func pmpAddPortMapping(d *pmpDevice, ctx context.Context, protocol nat.Protocol, internalPort, externalPort int, description string, duration time.Duration) (int, error)

//go:linkname pmpGetExternalIPv4Address github.com/syncthing/syncthing/lib/pmp.(*wrapper).GetExternalIPv4Address
func pmpGetExternalIPv4Address(d *pmpDevice, ctx context.Context) (net.IP, error)

// pmpLinkedMethods are the engine's methods the test calls by linker name.
var pmpLinkedMethods = map[string]any{
	"AddPortMapping":         pmpAddPortMapping,
	"GetExternalIPv4Address": pmpGetExternalIPv4Address,
}

func newPMPDevice(gateway net.IP) *pmpDevice {
	return &pmpDevice{
		renewal:   30 * time.Minute,
		localIP:   gateway,
		gatewayIP: gateway,
		client:    natpmp.NewClient(gateway), // the library's default: retries for about two minutes
	}
}

// requirePMPDeviceMirror fails the test unless the lib/pmp the build compiled
// matches pmpDevice and the linked declarations (pmpMirrorMismatch).
func requirePMPDeviceMirror(t *testing.T) {
	t.Helper()
	dir, err := pmpSourceDir()
	if err == nil {
		err = pmpMirrorMismatch(dir)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// The guard must fail closed on every change that would make the linked calls
// unsafe, also a NAT-PMP library under another import path whose package has
// the same name. Each case changes a copy of the engine's own source.
func TestIssue195_TheMirrorGuardFailsClosed(t *testing.T) {
	dir, err := pmpSourceDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := pmpMirrorMismatch(dir); err != nil {
		t.Fatalf("the engine's own lib/pmp: %v", err)
	}
	original, err := os.ReadFile(filepath.Join(dir, "pmp.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(original)

	for _, c := range []struct{ name, old, new, extraFile string }{
		{name: "the NAT-PMP library under another import path", old: `natpmp "github.com/jackpal/go-nat-pmp"`, new: `natpmp "github.com/jackpal/go-nat-pmp/v2"`},
		{name: "a package qualifier no import names", old: `natpmp "github.com/jackpal/go-nat-pmp"`, new: `"github.com/jackpal/go-nat-pmp"`},
		{name: "a dot import", old: `"github.com/syncthing/syncthing/lib/nat"`, new: `"github.com/syncthing/syncthing/lib/nat"` + "\n\t. \"strconv\""},
		{name: "fields reordered", old: "\tlocalIP   net.IP\n\tgatewayIP net.IP\n", new: "\tgatewayIP net.IP\n\tlocalIP   net.IP\n"},
		{name: "a field's type changed", old: "\trenewal   time.Duration\n", new: "\trenewal   int64\n"},
		{name: "an embedded field", old: "\tclient    *natpmp.Client\n}", new: "\tclient    *natpmp.Client\n\tnatpmp.Client\n}"},
		{name: "a changed signature", old: "GetExternalIPv4Address(ctx context.Context) (net.IP, error) {", new: "GetExternalIPv4Address(ctx context.Context) (net.IP, int, error) {"},
		{name: "a value receiver", old: "func (w *wrapper) AddPortMapping(", new: "func (w wrapper) AddPortMapping("},
		{name: "the type declared twice", extraFile: "package pmp\n\ntype wrapper struct{}\n"},
		{name: "a method declared twice", extraFile: "package pmp\n\nimport \"context\"\n\nfunc (w *wrapper) GetExternalIPv4Address(ctx context.Context) {}\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			changed := src
			if c.old != "" {
				if strings.Count(src, c.old) != 1 {
					t.Fatalf("lib/pmp no longer holds %q once: update this case", c.old)
				}
				changed = strings.Replace(src, c.old, c.new, 1)
			}
			tmp := t.TempDir()
			if err := os.WriteFile(filepath.Join(tmp, "pmp.go"), []byte(changed), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.extraFile != "" {
				if err := os.WriteFile(filepath.Join(tmp, "extra.go"), []byte(c.extraFile), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := pmpMirrorMismatch(tmp); err == nil {
				t.Fatal("the guard accepted it")
			}
		})
	}

	t.Run("a source it cannot read", func(t *testing.T) {
		if err := pmpMirrorMismatch(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatal("the guard accepted a directory that does not exist")
		}
	})
}

// pmpSourceDir returns the lib/pmp directory the build compiled: go.mod
// replaces the Syncthing module with one local copy, and the guard reads that
// copy. Anything else in go.mod fails closed.
func pmpSourceDir() (string, error) {
	data, err := os.ReadFile(filepath.Join("..", "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	var targets []string
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) == 4 && f[0] == "replace" && f[1] == "github.com/syncthing/syncthing" && f[2] == "=>" {
			targets = append(targets, f[3])
		}
	}
	if len(targets) != 1 || !(strings.HasPrefix(targets[0], "./") || strings.HasPrefix(targets[0], "../")) {
		return "", fmt.Errorf("go.mod replaces github.com/syncthing/syncthing with %q; the guard expects one local copy", targets)
	}
	return filepath.Join("..", filepath.FromSlash(targets[0]), "lib", "pmp"), nil
}

const pmpPackagePath = "github.com/syncthing/syncthing/lib/pmp"

// pmpMirrorMismatch returns nil only if the lib/pmp source in dir declares its
// device type exactly once, with the fields of pmpDevice in their order, and
// each linked method exactly once on *wrapper with the declared signature.
// Every type is compared by its import path, not its package name. It fails
// closed: a source it cannot read or parse, a qualifier it cannot resolve, a
// type expression it does not know, and any difference are errors.
func pmpMirrorMismatch(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read lib/pmp (run `make patch` first): %w", err)
	}

	var engineFields []string
	wrappers := 0
	engineMethods := map[string][]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("read lib/pmp/%s: %w", name, err)
		}
		imports, err := pmpFileImports(file)
		if err != nil {
			return fmt.Errorf("lib/pmp/%s: %w", name, err)
		}
		typeString := func(e ast.Expr) (string, error) { return pmpASTTypeString(e, imports) }

		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name.Name != "wrapper" {
						continue
					}
					wrappers++
					st, ok := ts.Type.(*ast.StructType)
					if !ok || ts.Assign.IsValid() || ts.TypeParams != nil {
						return fmt.Errorf("lib/pmp's wrapper is no longer a plain struct: %s", types.ExprString(ts.Type))
					}
					for _, f := range st.Fields.List {
						ft, err := typeString(f.Type)
						if err != nil {
							return fmt.Errorf("lib/pmp's wrapper: %w", err)
						}
						if len(f.Names) == 0 {
							engineFields = append(engineFields, "(embedded) "+ft)
						}
						for _, field := range f.Names {
							engineFields = append(engineFields, field.Name+" "+ft)
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil || len(d.Recv.List) != 1 {
					continue
				}
				if recv, err := typeString(d.Recv.List[0].Type); err != nil || recv != "*"+pmpPackagePath+".wrapper" {
					continue
				}
				sig, err := pmpASTSignature(d.Type, typeString)
				if err != nil {
					// Matters only for a linked method, which then mismatches.
					sig = "(cannot render: " + err.Error() + ")"
				}
				engineMethods[d.Name.Name] = append(engineMethods[d.Name.Name], sig)
			}
		}
	}
	if wrappers != 1 {
		return fmt.Errorf("lib/pmp declares its wrapper type %d times, the test's mirror expects 1: update pmpDevice", wrappers)
	}

	var mirrorFields []string
	mirror := reflect.TypeFor[pmpDevice]()
	for i := range mirror.NumField() {
		mirrorFields = append(mirrorFields, mirror.Field(i).Name+" "+pmpReflectTypeString(mirror.Field(i).Type))
	}
	if !slices.Equal(engineFields, mirrorFields) {
		return fmt.Errorf("lib/pmp's wrapper has the fields %q, the test's mirror %q: update pmpDevice", engineFields, mirrorFields)
	}

	for name, linked := range pmpLinkedMethods {
		ft := reflect.TypeOf(linked)
		if ft.NumIn() == 0 || ft.In(0) != reflect.TypeFor[*pmpDevice]() {
			return fmt.Errorf("the test links (*wrapper).%s without a *pmpDevice receiver first", name)
		}
		var in, out []string
		for i := 1; i < ft.NumIn(); i++ {
			in = append(in, pmpReflectTypeString(ft.In(i)))
		}
		for i := range ft.NumOut() {
			out = append(out, pmpReflectTypeString(ft.Out(i)))
		}
		want := "func(" + strings.Join(in, ", ") + ") (" + strings.Join(out, ", ") + ")"
		if got := engineMethods[name]; len(got) != 1 || got[0] != want {
			return fmt.Errorf("lib/pmp's (*wrapper).%s is %q, the test links it as %q: update the declaration", name, got, want)
		}
	}
	return nil
}

// pmpFileImports maps each package qualifier a file can use to its import
// path: the alias when the import has one, else the path's last element (the
// one before a /vN suffix). A dot import fails closed: its names cannot be
// told from the package's own.
func pmpFileImports(file *ast.File) (map[string]string, error) {
	imports := map[string]string{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		} else {
			elems := strings.Split(path, "/")
			name = elems[len(elems)-1]
			if len(elems) > 1 && len(name) > 1 && name[0] == 'v' && strings.Trim(name[1:], "0123456789") == "" {
				name = elems[len(elems)-2]
			}
		}
		switch name {
		case "_":
		case ".":
			return nil, fmt.Errorf("dot import of %q", path)
		default:
			imports[name] = path
		}
	}
	return imports, nil
}

// pmpASTTypeString renders a type expression of lib/pmp's source as
// pmpReflectTypeString renders the matching reflect.Type: a named type as
// "<import path>.<name>", a predeclared one by its name.
func pmpASTTypeString(e ast.Expr, imports map[string]string) (string, error) {
	switch x := e.(type) {
	case *ast.Ident:
		if _, ok := types.Universe.Lookup(x.Name).(*types.TypeName); ok {
			return x.Name, nil
		}
		return pmpPackagePath + "." + x.Name, nil
	case *ast.SelectorExpr:
		pkg, ok := x.X.(*ast.Ident)
		if !ok {
			return "", fmt.Errorf("unknown type expression %s", types.ExprString(e))
		}
		path, ok := imports[pkg.Name]
		if !ok {
			return "", fmt.Errorf("no import names the package %q in %s", pkg.Name, types.ExprString(e))
		}
		return path + "." + x.Sel.Name, nil
	case *ast.StarExpr:
		s, err := pmpASTTypeString(x.X, imports)
		return "*" + s, err
	case *ast.ArrayType:
		if x.Len != nil {
			return "", fmt.Errorf("unknown type expression %s", types.ExprString(e))
		}
		s, err := pmpASTTypeString(x.Elt, imports)
		return "[]" + s, err
	default:
		return "", fmt.Errorf("unknown type expression %s", types.ExprString(e))
	}
}

// pmpASTSignature renders a method's parameters and results, without its
// receiver, as "func(<params>) (<results>)".
func pmpASTSignature(ft *ast.FuncType, typeString func(ast.Expr) (string, error)) (string, error) {
	list := func(fields *ast.FieldList) ([]string, error) {
		var out []string
		if fields == nil {
			return out, nil
		}
		for _, f := range fields.List {
			s, err := typeString(f.Type)
			if err != nil {
				return nil, err
			}
			for range max(1, len(f.Names)) {
				out = append(out, s)
			}
		}
		return out, nil
	}
	in, err := list(ft.Params)
	if err != nil {
		return "", err
	}
	out, err := list(ft.Results)
	if err != nil {
		return "", err
	}
	return "func(" + strings.Join(in, ", ") + ") (" + strings.Join(out, ", ") + ")", nil
}

// pmpReflectTypeString renders t as pmpASTTypeString renders source; a kind
// it does not know renders as something no source expression matches.
func pmpReflectTypeString(t reflect.Type) string {
	switch {
	case t.Kind() == reflect.Pointer:
		return "*" + pmpReflectTypeString(t.Elem())
	case t.Name() != "" && t.PkgPath() != "":
		return t.PkgPath() + "." + t.Name()
	case t.Name() != "":
		return t.Name()
	case t.Kind() == reflect.Slice:
		return "[]" + pmpReflectTypeString(t.Elem())
	default:
		return "(unsupported) " + t.String()
	}
}

const natpmpTestMappedPort = 41250

var natpmpTestExternalIP = net.IPv4(203, 0, 113, 7)

// heldNATPMPGateway is a NAT-PMP gateway on loopback that takes requests but
// answers none until release, so a request stays in flight for as long as
// the test needs. NAT-PMP has a fixed port, so it needs 127.0.0.1:5351.
type heldNATPMPGateway struct {
	ip        net.IP
	conn      *net.UDPConn
	requested chan struct{} // closed on the first request
	served    chan struct{} // closed when serve returns

	mu       sync.Mutex
	released bool
	last     []byte
	from     *net.UDPAddr
}

func startHeldNATPMPGateway(t *testing.T) *heldNATPMPGateway {
	t.Helper()
	ip := net.IPv4(127, 0, 0, 1).To4()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: 5351})
	if err != nil {
		t.Fatalf("listen on the NAT-PMP port (another program holds it?): %v", err)
	}
	g := &heldNATPMPGateway{ip: ip, conn: conn, requested: make(chan struct{}), served: make(chan struct{})}
	go g.serve()
	t.Cleanup(func() {
		conn.Close()
		<-g.served
	})
	return g
}

func (g *heldNATPMPGateway) serve() {
	defer close(g.served)
	buf := make([]byte, 64)
	first := true
	for {
		n, from, err := g.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		request := slices.Clone(buf[:n])
		g.mu.Lock()
		g.last, g.from = request, from
		released := g.released
		g.mu.Unlock()
		if first {
			close(g.requested)
			first = false
		}
		if released {
			g.answer(request, from)
		}
	}
}

// release answers the last request at once, and every later one.
func (g *heldNATPMPGateway) release() {
	g.mu.Lock()
	g.released = true
	request, from := g.last, g.from
	g.mu.Unlock()
	if from != nil {
		g.answer(request, from)
	}
}

// answer sends a successful reply (RFC 6886) to an external-address request
// (opcode 0) or a mapping request (opcodes 1 and 2).
func (g *heldNATPMPGateway) answer(request []byte, to *net.UDPAddr) {
	if len(request) < 2 {
		return
	}
	var reply []byte
	switch op := request[1]; {
	case op == 0 && len(request) == 2:
		reply = make([]byte, 12)
		copy(reply[8:12], natpmpTestExternalIP.To4())
	case (op == 1 || op == 2) && len(request) == 12:
		reply = make([]byte, 16)
		copy(reply[8:10], request[4:6]) // internal port
		reply[10], reply[11] = byte(natpmpTestMappedPort>>8), byte(natpmpTestMappedPort&0xff)
		copy(reply[12:16], request[8:12]) // lifetime
	default:
		return
	}
	reply[1] = request[1] | 0x80
	_, _ = g.conn.WriteToUDP(reply, to)
}

// goroutineCreatedBy polls the goroutine dump for a goroutine that function
// started in goroutine parent and returns its ID, or "" after timeout. Like
// awaitGoroutineExit it synchronizes with nothing on purpose.
func goroutineCreatedBy(function, parent string, timeout time.Duration) string {
	marker := "\ncreated by " + function + " in goroutine " + parent + "\n"
	buf := make([]byte, 1<<16)
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		n := runtime.Stack(buf, true)
		for n == len(buf) {
			buf = make([]byte, 2*len(buf))
			n = runtime.Stack(buf, true)
		}
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(g+"\n", marker) {
				return strings.Fields(g)[1]
			}
		}
	}
	return ""
}

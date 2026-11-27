package bridge

import (
	"context"
	"errors"
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
// engine's source before any call, so a Syncthing bump that changes them
// fails the test instead of running a method on the wrong memory; a method
// the engine no longer defines on *wrapper fails the build at link time.
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

func newPMPDevice(gateway net.IP) *pmpDevice {
	return &pmpDevice{
		renewal:   30 * time.Minute,
		localIP:   gateway,
		gatewayIP: gateway,
		client:    natpmp.NewClient(gateway), // the library's default: retries for about two minutes
	}
}

// requirePMPDeviceMirror fails the test unless lib/pmp declares its device
// type exactly once, with the fields of pmpDevice in their order, and each
// linked method exactly once with the signature declared above. It fails
// closed: a source it cannot read or parse, and any difference, end the test
// before the first linked call. It reads the engine's source, so run `make
// patch` first.
func requirePMPDeviceMirror(t *testing.T) {
	t.Helper()
	dir := filepath.Join("..", "_syncthing_patched", "lib", "pmp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read lib/pmp (run `make patch` first): %v", err)
	}

	var engineFields []string
	wrappers := 0
	engineMethods := map[string][]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("read lib/pmp/%s: %v", name, err)
		}
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
						t.Fatalf("lib/pmp's wrapper is no longer a plain struct: %s", types.ExprString(ts.Type))
					}
					for _, f := range st.Fields.List {
						if len(f.Names) == 0 {
							engineFields = append(engineFields, "(embedded) "+types.ExprString(f.Type))
						}
						for _, field := range f.Names {
							engineFields = append(engineFields, field.Name+" "+types.ExprString(f.Type))
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv != nil && len(d.Recv.List) == 1 && types.ExprString(d.Recv.List[0].Type) == "*wrapper" {
					engineMethods[d.Name.Name] = append(engineMethods[d.Name.Name], astSignature("*wrapper", d.Type))
				}
			}
		}
	}
	if wrappers != 1 {
		t.Fatalf("lib/pmp declares its wrapper type %d times, the test's mirror expects 1: update pmpDevice", wrappers)
	}

	var mirrorFields []string
	mirror := reflect.TypeFor[pmpDevice]()
	for i := range mirror.NumField() {
		mirrorFields = append(mirrorFields, mirror.Field(i).Name+" "+mirror.Field(i).Type.String())
	}
	if !slices.Equal(engineFields, mirrorFields) {
		t.Fatalf("lib/pmp's wrapper has the fields %q, the test's mirror %q: update pmpDevice", engineFields, mirrorFields)
	}

	for name, linked := range map[string]any{
		"AddPortMapping":         pmpAddPortMapping,
		"GetExternalIPv4Address": pmpGetExternalIPv4Address,
	} {
		want := strings.Replace(reflect.TypeOf(linked).String(), "*"+mirror.String(), "*wrapper", 1)
		if got := engineMethods[name]; len(got) != 1 || got[0] != want {
			t.Fatalf("lib/pmp's (*wrapper).%s is %q, the test links it as %q: update the declaration", name, got, want)
		}
	}
}

// astSignature renders a method's type the way reflect renders a function
// that takes the receiver first.
func astSignature(receiver string, ft *ast.FuncType) string {
	list := func(fields *ast.FieldList) []string {
		var out []string
		if fields == nil {
			return out
		}
		for _, f := range fields.List {
			for range max(1, len(f.Names)) {
				out = append(out, types.ExprString(f.Type))
			}
		}
		return out
	}
	in := append([]string{receiver}, list(ft.Params)...)
	out := list(ft.Results)
	s := "func(" + strings.Join(in, ", ") + ")"
	switch len(out) {
	case 0:
	case 1:
		s += " " + out[0]
	default:
		s += " (" + strings.Join(out, ", ") + ")"
	}
	return s
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

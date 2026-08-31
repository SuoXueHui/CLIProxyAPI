package egress

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestEnsureAddressKeepsHealthyExistingAddress(t *testing.T) {
	if runtimeGOOS := os.Getenv("GOOS"); runtimeGOOS != "" && runtimeGOOS != "linux" {
		t.Skip("fake ip command test targets Linux command semantics")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ip")
	argsPath := filepath.Join(dir, "args")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$IP_ARGS_FILE\"\n" +
		"printf '%s\\n' '2: eth0    inet6 2001:db8::42/64 scope global nodad'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+originalPath)
	t.Setenv("IP_ARGS_FILE", argsPath)
	adopted, errEnsure := ensureAddress(Config{Enabled: true, Prefix: "2001:db8::/64", Interface: "eth0"}, net.ParseIP("2001:db8::42"))
	if errEnsure != nil {
		t.Fatalf("ensureAddress() error = %v", errEnsure)
	}
	if !adopted {
		t.Fatal("ensureAddress() did not adopt healthy existing address")
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(args)), "-6 -o addr show dev eth0"; got != want {
		t.Fatalf("ip invocations = %q, want only health check %q", got, want)
	}
}

func TestEnsureAddressRepairsDADFailedAddressWithNoDAD(t *testing.T) {
	if runtimeGOOS := os.Getenv("GOOS"); runtimeGOOS != "" && runtimeGOOS != "linux" {
		t.Skip("fake ip command test targets Linux command semantics")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ip")
	argsPath := filepath.Join(dir, "args")
	statePath := filepath.Join(dir, "state")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$IP_ARGS_FILE\"\n" +
		"case \"$*\" in\n" +
		"  *'addr show'*)\n" +
		"    if [ -f \"$IP_STATE_FILE\" ]; then\n" +
		"      printf '%s\\n' '2: eth0    inet6 2001:db8::42/64 scope global nodad'\n" +
		"    else\n" +
		"      printf '%s\\n' '2: eth0    inet6 2001:db8::42/64 scope global tentative dadfailed'\n" +
		"    fi\n" +
		"    ;;\n" +
		"  *'addr replace'*) touch \"$IP_STATE_FILE\" ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+originalPath)
	t.Setenv("IP_ARGS_FILE", argsPath)
	t.Setenv("IP_STATE_FILE", statePath)

	if err := EnsureAddress(Config{Enabled: true, Prefix: "2001:db8::/64", Interface: "eth0"}, net.ParseIP("2001:db8::42")); err != nil {
		t.Fatalf("EnsureAddress() error = %v", err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read fake ip arguments: %v", err)
	}
	want := []string{
		"-6 -o addr show dev eth0",
		"-6 addr replace 2001:db8::42/64 dev eth0 nodad",
		"-6 -o addr show dev eth0",
	}
	got := strings.Split(strings.TrimSpace(string(args)), "\n")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ip invocations = %#v, want %#v", got, want)
	}
}

func TestEnsureAddressRejectsAddressThatRemainsTentative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ip")
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *'addr show'*) printf '%s\\n' '2: eth0 inet6 2001:db8::42/64 scope global tentative' ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := EnsureAddress(Config{Enabled: true, Prefix: "2001:db8::/64", Interface: "eth0"}, net.ParseIP("2001:db8::42"))
	if err == nil || !strings.Contains(err.Error(), "tentative") {
		t.Fatalf("EnsureAddress() error = %v, want tentative state error", err)
	}
}

type fakeAddressManager struct {
	ensured []string
	removed []string
	err     error
	owned   bool
}

func (m *fakeAddressManager) Ensure(cfg Config, ip net.IP) (bool, error) {
	m.ensured = append(m.ensured, cfg.Prefix+"|"+ip.String())
	return m.owned, m.err
}

func (m *fakeAddressManager) Remove(cfg Config, ip net.IP) error {
	m.removed = append(m.removed, cfg.Prefix+"|"+ip.String())
	return m.err
}

func TestControllerPreservesCollisionStateAcrossIncrementalAssignments(t *testing.T) {
	manager := &fakeAddressManager{owned: true}
	controller, err := newController(Config{Enabled: true, Prefix: "2001:db8::/124"}, manager)
	if err != nil {
		t.Fatal(err)
	}
	first, err := controller.Assign("auth-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := controller.Assign("auth-2")
	if err != nil {
		t.Fatal(err)
	}
	if first.Equal(second) {
		t.Fatalf("incremental assignments collided at %s", first)
	}
}

func TestControllerReleaseRemovesOnlyManagedAssignment(t *testing.T) {
	manager := &fakeAddressManager{owned: true}
	controller, err := newController(Config{Enabled: true, Prefix: "2001:db8::/120"}, manager)
	if err != nil {
		t.Fatal(err)
	}
	ip, err := controller.Assign("managed")
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.Release("unknown"); err != nil {
		t.Fatal(err)
	}
	if len(manager.removed) != 0 {
		t.Fatalf("unknown release removed addresses: %#v", manager.removed)
	}
	if err = controller.Release("managed"); err != nil {
		t.Fatal(err)
	}
	want := []string{"2001:db8::/120|" + ip.String()}
	if !reflect.DeepEqual(manager.removed, want) {
		t.Fatalf("removed = %#v, want %#v", manager.removed, want)
	}
}

func TestControllerAdoptsHealthyExistingAddressForLifecycleCleanup(t *testing.T) {
	manager := &fakeAddressManager{owned: true}
	controller, err := newController(Config{Enabled: true, Prefix: "2001:db8::/120"}, manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = controller.Assign("preexisting"); err != nil {
		t.Fatal(err)
	}
	if err = controller.Release("preexisting"); err != nil {
		t.Fatal(err)
	}
	if len(manager.removed) != 1 {
		t.Fatalf("release removed %d adopted addresses, want 1", len(manager.removed))
	}
	if reserved, ok := controller.allocator.Lookup("preexisting"); ok || reserved != nil {
		t.Fatalf("release retained adopted allocator reservation: %s, %v", reserved, ok)
	}
}

func TestControllerDoesNotForgetAssignmentWhenRemovalFails(t *testing.T) {
	manager := &fakeAddressManager{owned: true}
	controller, err := newController(Config{Enabled: true, Prefix: "2001:db8::/120"}, manager)
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := controller.Assign("auth")
	if err != nil {
		t.Fatal(err)
	}
	manager.err = errors.New("permission denied")
	if err = controller.Release("auth"); err == nil {
		t.Fatal("Release() error = nil, want removal failure")
	}
	manager.err = nil
	lookup, ok := controller.Lookup("auth")
	if !ok || !lookup.Equal(assigned) {
		t.Fatalf("failed release lost managed assignment: %s, %v", lookup, ok)
	}
}

func TestDisabledAllocatorIgnoresInvalidConfiguration(t *testing.T) {
	a, err := NewAllocator(Config{Prefix: "not-a-cidr"})
	if err != nil {
		t.Fatalf("NewAllocator() error = %v", err)
	}
	if a.Enabled() {
		t.Fatal("disabled allocator reports enabled")
	}
	if ip, errResolve := a.Resolve("auth-1"); errResolve != nil || ip != nil {
		t.Fatalf("Resolve() = %v, %v; want nil, nil", ip, errResolve)
	}
}

func TestNewAllocatorValidatesConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "empty prefix", cfg: Config{Enabled: true}},
		{name: "ipv4 prefix", cfg: Config{Enabled: true, Prefix: "192.0.2.0/24"}},
		{name: "invalid mode", cfg: Config{Enabled: true, Mode: "random", Prefix: "2001:db8::/120"}},
		{name: "manual ipv4", cfg: Config{Enabled: true, Prefix: "2001:db8::/120", Manual: map[string]string{"a": "192.0.2.1"}}},
		{name: "manual outside prefix", cfg: Config{Enabled: true, Prefix: "2001:db8::/120", Manual: map[string]string{"a": "2001:db8::1:1"}}},
		{name: "manual duplicate address", cfg: Config{Enabled: true, Prefix: "2001:db8::/120", Manual: map[string]string{"a": "2001:db8::1", "b": "2001:db8::1"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewAllocator(tt.cfg); err == nil {
				t.Fatal("NewAllocator() error = nil, want validation error")
			}
		})
	}
}

func TestResolveManualAndStableAutomaticAssignments(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Prefix:  "2001:db8:abcd::/120",
		Manual:  map[string]string{"manual-auth": "2001:db8:abcd::42"},
	}
	a, err := NewAllocator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := a.Resolve("manual-auth")
	if err != nil || manual.String() != "2001:db8:abcd::42" {
		t.Fatalf("manual Resolve() = %v, %v", manual, err)
	}
	auto1, err := a.Resolve("auto-auth")
	if err != nil {
		t.Fatal(err)
	}
	auto2, err := a.Resolve("auto-auth")
	if err != nil {
		t.Fatal(err)
	}
	if !auto1.Equal(auto2) {
		t.Fatalf("automatic address changed: %s -> %s", auto1, auto2)
	}
	prefix, _ := ParseIPv6Prefix("2001:db8:abcd::/120")
	if !prefix.Contains(auto1) {
		t.Fatalf("automatic address %s is outside %s", auto1, prefix)
	}
	// A fresh allocator produces the same address because the first candidate
	// is derived from auth ID rather than process-local allocation order.
	fresh, err := NewAllocator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	freshAuto, err := fresh.Resolve("auto-auth")
	if err != nil || !freshAuto.Equal(auto1) {
		t.Fatalf("fresh allocator address = %s, %v; want %s", freshAuto, err, auto1)
	}
	lookup, ok := a.Lookup("auto-auth")
	if !ok || !lookup.Equal(auto1) {
		t.Fatalf("Lookup() = %s, %v", lookup, ok)
	}
	if _, ok = a.Lookup("unknown"); ok {
		t.Fatal("Lookup() allocated an unknown account")
	}
}

func TestManualModeRequiresExplicitMapping(t *testing.T) {
	a, err := NewAllocator(Config{
		Enabled: true,
		Mode:    ModeManual,
		Prefix:  "2001:db8::/120",
		Manual:  map[string]string{"manual": "2001:db8::1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Resolve("missing"); err == nil {
		t.Fatal("manual mode Resolve() error = nil")
	}
}

func TestAutomaticAssignmentAvoidsManualAddress(t *testing.T) {
	// The candidate for this auth is deterministic; reserve a range of likely
	// values by using a /128 prefix and verify the allocator still rejects an
	// exhausted address space rather than returning an out-of-prefix address.
	a, err := NewAllocator(Config{
		Enabled: true,
		Prefix:  "2001:db8::/128",
		Manual:  map[string]string{"reserved": "2001:db8::"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Resolve("auto"); err == nil {
		t.Fatal("Resolve() unexpectedly succeeded in exhausted /128 prefix")
	}
}

func TestAutomaticAssignmentSkipsDockerInfrastructureAddresses(t *testing.T) {
	a, err := NewAllocator(Config{Enabled: true, Prefix: "2001:db8::/120"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		ip, resolveErr := a.Resolve("account-" + string(rune('a'+i)))
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		if ip.String() == "2001:db8::" || ip.String() == "2001:db8::1" || ip.String() == "2001:db8::2" {
			t.Fatalf("Resolve() returned Docker infrastructure address %s", ip)
		}
	}
}

func TestAllocatorConcurrentResolve(t *testing.T) {
	a, err := NewAllocator(Config{Enabled: true, Prefix: "2001:db8::/112"})
	if err != nil {
		t.Fatal(err)
	}
	const count = 32
	addresses := make(chan net.IP, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ip, resolveErr := a.Resolve("auth-" + string(rune('a'+i)))
			if resolveErr != nil {
				t.Errorf("Resolve() error = %v", resolveErr)
				return
			}
			addresses <- ip
		}(i)
	}
	wg.Wait()
	close(addresses)
	seen := make(map[string]struct{}, count)
	for ip := range addresses {
		if _, exists := seen[ip.String()]; exists {
			t.Fatalf("duplicate automatically assigned address %s", ip)
		}
		seen[ip.String()] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("assigned %d addresses, want %d", len(seen), count)
	}
}

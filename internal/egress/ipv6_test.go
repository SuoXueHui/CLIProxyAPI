package egress

import (
	"net"
	"sync"
	"testing"
)

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

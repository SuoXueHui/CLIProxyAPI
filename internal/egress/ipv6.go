// Package egress contains account-scoped outbound address allocation helpers.
package egress

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	// ModeAuto allocates an address for accounts that do not have a manual entry.
	ModeAuto = "auto"
	// ModeManual requires every resolved account to have a manual address entry.
	ModeManual = "manual"
)

// Config controls account-scoped IPv6 address allocation.
//
// The feature is opt-in. When Enabled is false, Prefix and Manual are ignored
// so adding this block to an existing configuration cannot change startup
// behavior until the operator explicitly enables it.
type Config struct {
	Enabled   bool              `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Mode      string            `yaml:"mode,omitempty" json:"mode,omitempty"`
	Prefix    string            `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	Interface string            `yaml:"interface,omitempty" json:"interface,omitempty"`
	Manual    map[string]string `yaml:"manual,omitempty" json:"manual,omitempty"`
}

// ParseIPv6Prefix parses and canonicalizes an IPv6 CIDR prefix.
// IPv4-mapped addresses and bare addresses without a prefix length are
// rejected because the allocator must know exactly which host bits it owns.
func ParseIPv6Prefix(raw string) (*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("ipv6 egress prefix is empty")
	}
	ip, network, err := net.ParseCIDR(raw)
	if err != nil {
		return nil, fmt.Errorf("parse ipv6 egress prefix %q: %w", raw, err)
	}
	if ip.To4() != nil || network.IP.To16() == nil {
		return nil, fmt.Errorf("ipv6 egress prefix %q is not an IPv6 prefix", raw)
	}
	// net.ParseCIDR returns a 4-byte IPNet for IPv4 and a 16-byte one for IPv6;
	// normalize the latter so all address arithmetic uses one representation.
	network.IP = network.IP.To16()
	return network, nil
}

// Allocator resolves stable account IDs to IPv6 addresses.
type Allocator struct {
	enabled    bool
	mode       string
	prefix     *net.IPNet
	prefixBits int
	hostBits   int
	manual     map[string]net.IP
	reserved   map[string]string   // canonical IP -> account ID (manual entries)
	autoSkip   map[string]struct{} // Docker network addresses unavailable to auto allocation
	auto       map[string]net.IP
	mu         sync.Mutex
}

const maxAutoProbeAttempts = 1 << 20

// NewAllocator validates cfg and returns an allocator. Disabled allocators
// return nil addresses from Resolve without parsing or validating unused data.
func NewAllocator(cfg Config) (*Allocator, error) {
	a := &Allocator{
		enabled:  cfg.Enabled,
		mode:     strings.ToLower(strings.TrimSpace(cfg.Mode)),
		manual:   make(map[string]net.IP),
		reserved: make(map[string]string),
		autoSkip: make(map[string]struct{}),
		auto:     make(map[string]net.IP),
	}
	if !a.enabled {
		return a, nil
	}
	if a.mode == "" {
		a.mode = ModeAuto
	}
	if strings.TrimSpace(cfg.Interface) == "" {
		cfg.Interface = "eth0"
	}
	if a.mode != ModeAuto && a.mode != ModeManual {
		return nil, fmt.Errorf("invalid ipv6 egress mode %q: want %q or %q", cfg.Mode, ModeAuto, ModeManual)
	}
	network, err := ParseIPv6Prefix(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	a.prefix = network
	a.prefixBits, a.hostBits = prefixBitCounts(network)
	for rawID, rawIP := range cfg.Manual {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, fmt.Errorf("ipv6 egress manual mapping has an empty auth ID")
		}
		if _, exists := a.manual[id]; exists {
			return nil, fmt.Errorf("duplicate ipv6 egress manual auth ID %q", id)
		}
		ip := net.ParseIP(strings.TrimSpace(rawIP))
		if ip == nil || ip.To4() != nil {
			return nil, fmt.Errorf("ipv6 egress manual address for auth %q is not an IPv6 address: %q", id, rawIP)
		}
		ip = ip.To16()
		if !network.Contains(ip) {
			return nil, fmt.Errorf("ipv6 egress manual address %q for auth %q is outside prefix %s", ip, id, network.String())
		}
		key := ip.String()
		if previous, exists := a.reserved[key]; exists {
			return nil, fmt.Errorf("duplicate ipv6 egress manual address %q for auths %q and %q", ip, previous, id)
		}
		a.manual[id] = cloneIP(ip)
		a.reserved[key] = id
	}
	// A Docker IPv6 bridge normally reserves the network address, gateway, and
	// first container address. Keep these out of automatic allocation while
	// allowing an operator to use a deliberate manual mapping when necessary.
	for host := uint64(0); host <= 2 && host < (uint64(1)<<minUint(a.hostBits, 63)); host++ {
		ip := prefixAddress(a.prefix, host)
		a.autoSkip[ip.String()] = struct{}{}
	}
	return a, nil
}

// EnsureAddress adds an allocated IPv6 to the configured interface inside the
// CPA network namespace. Docker assigns only one address to a container; this
// step makes additional per-account addresses bindable by net.Dialer. Existing
// addresses are treated as success so hot reloads remain idempotent.
func EnsureAddress(cfg Config, ip net.IP) error {
	if !cfg.Enabled || ip == nil {
		return nil
	}
	network, err := ParseIPv6Prefix(cfg.Prefix)
	if err != nil {
		return err
	}
	if !network.Contains(ip) {
		return fmt.Errorf("IPv6 egress address %s is outside prefix %s", ip, network.String())
	}
	iface := strings.TrimSpace(cfg.Interface)
	if iface == "" {
		iface = interfaceForPrefix(network)
	}
	prefixBits, _ := network.Mask.Size()
	address := ip.String() + "/" + strconv.Itoa(prefixBits)
	cmd := exec.Command("ip", "-6", "addr", "add", address, "dev", iface)
	if output, errRun := cmd.CombinedOutput(); errRun != nil {
		lowerOutput := strings.ToLower(string(output))
		if strings.Contains(lowerOutput, "file exists") || strings.Contains(lowerOutput, "address already assigned") {
			return nil
		}
		return fmt.Errorf("add IPv6 egress address %s on %s: %w (%s)", ip, iface, errRun, strings.TrimSpace(string(output)))
	}
	return nil
}

// interfaceForPrefix finds the interface that already owns an address inside
// the configured Docker IPv6 network. Compose can attach networks in an order
// that makes the IPv6 bridge eth1 (or another name), so hard-coding eth0 would
// fail even though the route and capability are correct.
func interfaceForPrefix(network *net.IPNet) string {
	if network != nil {
		if interfaces, errInterfaces := net.Interfaces(); errInterfaces == nil {
			for _, iface := range interfaces {
				addresses, errAddrs := iface.Addrs()
				if errAddrs != nil {
					continue
				}
				for _, address := range addresses {
					var ip net.IP
					switch value := address.(type) {
					case *net.IPNet:
						ip = value.IP
					case *net.IPAddr:
						ip = value.IP
					}
					if ip != nil && ip.To4() == nil && network.Contains(ip) {
						return iface.Name
					}
				}
			}
		}
	}
	return "eth0"
}

// Enabled reports whether this allocator can assign addresses.
func (a *Allocator) Enabled() bool { return a != nil && a.enabled }

// Resolve returns the stable IPv6 assigned to authID. Manual mappings always
// take precedence. Automatic assignments are cached to avoid changing an
// address during the allocator lifetime, while the hash seed keeps normal
// assignments stable across restarts.
func (a *Allocator) Resolve(authID string) (net.IP, error) {
	if a == nil || !a.enabled {
		return nil, nil
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil, fmt.Errorf("ipv6 egress auth ID is empty")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if ip, ok := a.manual[authID]; ok {
		return cloneIP(ip), nil
	}
	if a.mode == ModeManual {
		return nil, fmt.Errorf("no manual ipv6 egress address configured for auth %q", authID)
	}
	if ip, ok := a.auto[authID]; ok {
		return cloneIP(ip), nil
	}
	// Detect a genuinely exhausted prefix before probing candidates. This is
	// especially important for /128 prefixes, where every probe is identical.
	capacity := new(big.Int).Lsh(big.NewInt(1), uint(a.hostBits))
	if capacity.Cmp(big.NewInt(int64(len(a.reserved)+len(a.auto)))) <= 0 {
		return nil, fmt.Errorf("ipv6 egress prefix %s has no unassigned addresses", a.prefix)
	}
	// Probe deterministic candidates on the rare occasion that two auth IDs
	// hash to the same host bits or a candidate is occupied by a manual entry.
	maxAttempts := uint64(maxAutoProbeAttempts)
	if a.hostBits < 20 {
		maxAttempts = uint64(1) << uint(a.hostBits)
	}
	for attempt := uint64(0); attempt < maxAttempts; attempt++ {
		ip := a.candidate(authID, attempt)
		key := ip.String()
		if _, skip := a.autoSkip[key]; skip {
			continue
		}
		if owner, used := a.reserved[key]; used && owner != authID {
			continue
		}
		collision := false
		for owner, assigned := range a.auto {
			if owner != authID && assigned.Equal(ip) {
				collision = true
				break
			}
		}
		if collision {
			continue
		}
		a.auto[authID] = cloneIP(ip)
		return cloneIP(ip), nil
	}
	return nil, fmt.Errorf("ipv6 egress prefix %s has no available address for auth %q", a.prefix.String(), authID)
}

// Lookup returns an address only when it has already been resolved or is a
// configured manual mapping. It does not allocate a new automatic address.
func (a *Allocator) Lookup(authID string) (net.IP, bool) {
	if a == nil || !a.enabled {
		return nil, false
	}
	authID = strings.TrimSpace(authID)
	a.mu.Lock()
	defer a.mu.Unlock()
	if ip, ok := a.manual[authID]; ok {
		return cloneIP(ip), true
	}
	ip, ok := a.auto[authID]
	if !ok {
		return nil, false
	}
	return cloneIP(ip), true
}

// Release forgets an automatic assignment. Manual mappings remain reserved so
// a temporarily disabled account cannot cause another account to take it.
func (a *Allocator) Release(authID string) {
	if a == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	a.mu.Lock()
	delete(a.auto, authID)
	a.mu.Unlock()
}

// Assignments returns a stable snapshot of all configured and allocated
// addresses, suitable for diagnostics or management APIs.
func (a *Allocator) Assignments() map[string]net.IP {
	result := make(map[string]net.IP)
	if a == nil || !a.enabled {
		return result
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, ip := range a.manual {
		result[id] = cloneIP(ip)
	}
	for id, ip := range a.auto {
		if _, manual := result[id]; !manual {
			result[id] = cloneIP(ip)
		}
	}
	return result
}

// ManualAuthIDs returns manual mapping IDs in deterministic order.
func (a *Allocator) ManualAuthIDs() []string {
	if a == nil || !a.enabled {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, 0, len(a.manual))
	for id := range a.manual {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func prefixBitCounts(network *net.IPNet) (prefixBits, hostBits int) {
	prefixBits, bits := network.Mask.Size()
	if bits != 128 {
		return 0, 0
	}
	return prefixBits, bits - prefixBits
}

func prefixAddress(network *net.IPNet, host uint64) net.IP {
	base := new(big.Int).SetBytes(network.IP.To16())
	base.Add(base, new(big.Int).SetUint64(host))
	return net.IP(base.FillBytes(make([]byte, net.IPv6len)))
}

func minUint(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func (a *Allocator) candidate(authID string, attempt uint64) net.IP {
	var suffix [8]byte
	binary.BigEndian.PutUint64(suffix[:], attempt)
	seed := make([]byte, 0, len(authID)+len(suffix))
	seed = append(seed, authID...)
	seed = append(seed, suffix[:]...)
	digest := sha256.Sum256(seed)

	base := new(big.Int).SetBytes(a.prefix.IP.To16())
	host := new(big.Int).SetBytes(digest[:])
	space := new(big.Int).Lsh(big.NewInt(1), uint(a.hostBits))
	host.Mod(host, space)
	base.Add(base, host)
	address := base.FillBytes(make([]byte, net.IPv6len))
	return net.IP(address)
}

func cloneIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	return append(net.IP(nil), ip...)
}

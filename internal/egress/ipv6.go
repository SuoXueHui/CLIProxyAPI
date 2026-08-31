// Package egress contains account-scoped outbound address allocation helpers.
package egress

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os/exec"
	"reflect"
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

// Address ownership is exclusive to one network namespace. The nodad flag only
// disables kernel duplicate-address detection; it is not a lease or a
// blue/green ownership protocol. Operators must remove an address from the old
// container before a replacement container starts using the same prefix.

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

// EnsureAddress adds or repairs an allocated IPv6 on the configured interface
// inside the CPA network namespace. Healthy existing addresses are left
// untouched so hot reloads remain lossless. Tentative or DAD-failed addresses
// are replaced with nodad and verified before they can be used by net.Dialer.
func EnsureAddress(cfg Config, ip net.IP) error {
	_, err := ensureAddress(cfg, ip)
	return err
}

func ensureAddress(cfg Config, ip net.IP) (bool, error) {
	if !cfg.Enabled || ip == nil {
		return false, nil
	}
	network, err := ParseIPv6Prefix(cfg.Prefix)
	if err != nil {
		return false, err
	}
	if !network.Contains(ip) {
		return false, fmt.Errorf("IPv6 egress address %s is outside prefix %s", ip, network.String())
	}
	iface := strings.TrimSpace(cfg.Interface)
	if iface == "" {
		iface = interfaceForPrefix(network)
	}
	prefixBits, _ := network.Mask.Size()
	address := ip.String() + "/" + strconv.Itoa(prefixBits)
	state, errState := readAddressState(iface, ip)
	if errState != nil {
		return false, errState
	}
	if state.exists && !state.unusable() {
		// A healthy address in this network namespace can be adopted by the
		// current process so later auth deletion or config disable can remove it.
		// This does not provide ownership exclusion across container namespaces.
		return true, nil
	}
	// Docker bridge interfaces can retain tentative or dadfailed state after a
	// failed handoff. replace is idempotent for both absent and existing
	// allocator-owned addresses, while nodad prevents a new DAD cycle.
	if errReplace := runIP("replace IPv6 egress address", "-6", "addr", "replace", address, "dev", iface, "nodad"); errReplace != nil {
		return false, errReplace
	}
	state, errState = readAddressState(iface, ip)
	if errState != nil {
		return false, errState
	}
	if state.exists && !state.unusable() {
		return true, nil
	}
	if state.unusable() {
		// Some kernels retain failed DAD state across replace. The address is
		// already unusable, so delete and recreate it before the final check.
		if errDelete := runIP("delete unusable IPv6 egress address", "-6", "addr", "del", address, "dev", iface); errDelete != nil {
			return false, errDelete
		}
		if errAdd := runIP("re-add IPv6 egress address", "-6", "addr", "add", address, "dev", iface, "nodad"); errAdd != nil {
			return false, errAdd
		}
		state, errState = readAddressState(iface, ip)
		if errState != nil {
			return false, errState
		}
	}
	if !state.exists {
		return false, fmt.Errorf("IPv6 egress address %s is absent after configuration on %s", ip, iface)
	}
	if state.unusable() {
		return false, fmt.Errorf("IPv6 egress address %s remains %s on %s", ip, strings.Join(state.badFlags, ","), iface)
	}
	return true, nil
}

type addressState struct {
	exists   bool
	badFlags []string
}

func (s addressState) unusable() bool { return len(s.badFlags) > 0 }

func readAddressState(iface string, target net.IP) (addressState, error) {
	cmd := exec.Command("ip", "-6", "-o", "addr", "show", "dev", iface)
	output, errRun := cmd.CombinedOutput()
	if errRun != nil {
		return addressState{}, fmt.Errorf("inspect IPv6 egress addresses on %s: %w (%s)", iface, errRun, strings.TrimSpace(string(output)))
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		for index, field := range fields {
			if field != "inet6" || index+1 >= len(fields) {
				continue
			}
			ip, _, errCIDR := net.ParseCIDR(fields[index+1])
			if errCIDR != nil || !ip.Equal(target) {
				continue
			}
			state := addressState{exists: true}
			for _, flag := range fields[index+2:] {
				switch strings.ToLower(flag) {
				case "tentative", "dadfailed":
					state.badFlags = append(state.badFlags, strings.ToLower(flag))
				}
			}
			return state, nil
		}
	}
	return addressState{}, nil
}

func runIP(action string, args ...string) error {
	cmd := exec.Command("ip", args...)
	output, errRun := cmd.CombinedOutput()
	if errRun != nil {
		return fmt.Errorf("%s: %w (%s)", action, errRun, strings.TrimSpace(string(output)))
	}
	return nil
}

func removeAddress(cfg Config, ip net.IP) error {
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
	state, errState := readAddressState(iface, ip)
	if errState != nil {
		return errState
	}
	if !state.exists {
		return nil
	}
	prefixBits, _ := network.Mask.Size()
	address := ip.String() + "/" + strconv.Itoa(prefixBits)
	return runIP("remove IPv6 egress address", "-6", "addr", "del", address, "dev", iface)
}

type addressManager interface {
	Ensure(Config, net.IP) (bool, error)
	Remove(Config, net.IP) error
}

type systemAddressManager struct{}

func (systemAddressManager) Ensure(cfg Config, ip net.IP) (bool, error) {
	return ensureAddress(cfg, ip)
}
func (systemAddressManager) Remove(cfg Config, ip net.IP) error { return removeAddress(cfg, ip) }

// Controller owns one process-local allocator and the addresses successfully
// attached through it. Reusing this owner across incremental auth updates keeps
// collision tracking intact. Reconciliation removes only addresses recorded by
// this controller; Docker-assigned and unrelated manual addresses are never
// discovered or garbage-collected. This process-local ownership still requires
// deployment-level exclusion between multiple containers.
type Controller struct {
	mu        sync.Mutex
	cfg       Config
	allocator *Allocator
	managed   map[string]net.IP
	addresses addressManager
}

// NewController creates a long-lived process owner for IPv6 egress state.
func NewController(cfg Config) (*Controller, error) {
	return newController(cfg, systemAddressManager{})
}

func newController(cfg Config, addresses addressManager) (*Controller, error) {
	if addresses == nil {
		addresses = systemAddressManager{}
	}
	allocator, err := NewAllocator(cfg)
	if err != nil {
		return nil, err
	}
	return &Controller{
		cfg:       cloneConfig(cfg),
		allocator: allocator,
		managed:   make(map[string]net.IP),
		addresses: addresses,
	}, nil
}

// Assign resolves and attaches the stable address for authID.
func (c *Controller) Assign(authID string) (net.IP, error) {
	if c == nil {
		return nil, nil
	}
	authID = strings.TrimSpace(authID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.allocator == nil || !c.allocator.Enabled() {
		return nil, nil
	}
	ip, err := c.allocator.Resolve(authID)
	if err != nil || ip == nil {
		return ip, err
	}
	owned, errEnsure := c.addresses.Ensure(c.cfg, ip)
	if errEnsure != nil {
		return nil, errEnsure
	}
	if owned {
		c.managed[authID] = cloneIP(ip)
	}
	return cloneIP(ip), nil
}

// Release removes an address only when this controller previously attached it.
func (c *Controller) Release(authID string) error {
	if c == nil {
		return nil
	}
	authID = strings.TrimSpace(authID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if ip, owned := c.managed[authID]; owned {
		if errRemove := c.addresses.Remove(c.cfg, ip); errRemove != nil {
			return errRemove
		}
		delete(c.managed, authID)
		c.allocator.Release(authID)
	}
	// Unknown assignments are left untouched because the controller has no
	// evidence that their kernel address belongs to this lifecycle.
	return nil
}

// Lookup returns only an address that this controller successfully attached.
func (c *Controller) Lookup(authID string) (net.IP, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ip, ok := c.managed[strings.TrimSpace(authID)]
	return cloneIP(ip), ok
}

// Equivalent reports whether cfg describes the controller's active allocation domain.
func (c *Controller) Equivalent(cfg Config) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return configEqual(c.cfg, cfg)
}

// ManagedAssignments returns addresses owned or adopted by this controller.
func (c *Controller) ManagedAssignments() map[string]net.IP {
	result := make(map[string]net.IP)
	if c == nil {
		return result
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for authID, ip := range c.managed {
		result[authID] = cloneIP(ip)
	}
	return result
}

// Close removes managed addresses except those retained by a replacement controller.
// Retention is matched by address because a config transition can preserve an address
// while changing its auth mapping representation.
func (c *Controller) CloseExcept(retained map[string]net.IP) error {
	if c == nil {
		return nil
	}
	retainedIPs := make(map[string]struct{}, len(retained))
	for _, ip := range retained {
		if ip != nil {
			retainedIPs[ip.String()] = struct{}{}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := sortedAssignmentIDs(c.managed)
	errs := make([]error, 0)
	for _, authID := range ids {
		ip := c.managed[authID]
		if _, keep := retainedIPs[ip.String()]; !keep {
			if errRemove := c.addresses.Remove(c.cfg, ip); errRemove != nil {
				errs = append(errs, fmt.Errorf("remove IPv6 egress for auth %q: %w", authID, errRemove))
				continue
			}
		}
		delete(c.managed, authID)
		c.allocator.Release(authID)
	}
	return errors.Join(errs...)
}

// Close removes every address owned or adopted by this controller.
func (c *Controller) Close() error {
	return c.CloseExcept(nil)
}

func cloneConfig(cfg Config) Config {
	clone := cfg
	if cfg.Manual != nil {
		clone.Manual = make(map[string]string, len(cfg.Manual))
		for authID, ip := range cfg.Manual {
			clone.Manual[authID] = ip
		}
	}
	return clone
}

func configEqual(left, right Config) bool {
	return reflect.DeepEqual(normalizeConfig(left), normalizeConfig(right))
}

func normalizeConfig(cfg Config) Config {
	if !cfg.Enabled {
		return Config{}
	}
	normalized := cloneConfig(cfg)
	normalized.Mode = strings.ToLower(strings.TrimSpace(normalized.Mode))
	if normalized.Mode == "" {
		normalized.Mode = ModeAuto
	}
	normalized.Interface = strings.TrimSpace(normalized.Interface)
	if network, err := ParseIPv6Prefix(normalized.Prefix); err == nil {
		normalized.Prefix = network.String()
	} else {
		normalized.Prefix = strings.TrimSpace(normalized.Prefix)
	}
	if len(normalized.Manual) == 0 {
		normalized.Manual = nil
		return normalized
	}
	manual := make(map[string]string, len(normalized.Manual))
	for authID, rawIP := range normalized.Manual {
		ip := net.ParseIP(strings.TrimSpace(rawIP))
		if ip != nil {
			rawIP = ip.String()
		}
		manual[strings.TrimSpace(authID)] = strings.TrimSpace(rawIP)
	}
	normalized.Manual = manual
	return normalized
}

func sortedAssignmentIDs(assignments map[string]net.IP) []string {
	ids := make([]string, 0, len(assignments))
	for authID := range assignments {
		ids = append(ids, authID)
	}
	sort.Strings(ids)
	return ids
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

package synthesizer

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ensureEgressAllocator creates the allocator lazily so all auth entries built
// from one synthesis pass share collision tracking and manual reservations.
func ensureEgressAllocator(ctx *SynthesisContext) error {
	if ctx == nil || ctx.Config == nil || ctx.EgressAllocator != nil {
		return nil
	}
	allocator, err := egress.NewAllocator(ctx.Config.IPv6Egress)
	if err != nil {
		return err
	}
	ctx.EgressAllocator = allocator
	return nil
}

// applyEgressIPv6 assigns the stable source IPv6 to a synthesized auth.
// Empty output means the opt-in feature is disabled; an invalid enabled
// configuration is returned to the caller instead of silently falling back.
func applyEgressIPv6(ctx *SynthesisContext, auth *coreauth.Auth) error {
	if auth == nil || ctx == nil || ctx.Config == nil {
		return nil
	}
	if err := ensureEgressAllocator(ctx); err != nil {
		return err
	}
	if ctx.EgressAllocator == nil || !ctx.EgressAllocator.Enabled() {
		return nil
	}
	ip, err := ctx.EgressAllocator.Resolve(auth.ID)
	if err != nil {
		return err
	}
	if ip != nil {
		auth.EgressIPv6 = ip.String()
	}
	return nil
}

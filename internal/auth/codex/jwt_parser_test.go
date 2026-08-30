package codex

import "testing"

func TestJWTClaimsMemberFingerprintUsesStableChatGPTUserID(t *testing.T) {
	first := (&JWTClaims{
		Iss: "https://issuer.example",
		Sub: "subject-a",
		CodexAuthInfo: CodexAuthInfo{
			ChatgptUserID: "member-a",
		},
	}).MemberFingerprint()
	second := (&JWTClaims{
		Iss: "https://issuer.example",
		Sub: "rotated-subject",
		CodexAuthInfo: CodexAuthInfo{
			ChatgptUserID: "member-a",
		},
	}).MemberFingerprint()
	other := (&JWTClaims{
		Iss: "https://issuer.example",
		CodexAuthInfo: CodexAuthInfo{
			ChatgptUserID: "member-b",
		},
	}).MemberFingerprint()

	if first == "" || first != second {
		t.Fatalf("stable member fingerprints = %q and %q", first, second)
	}
	if first == other {
		t.Fatalf("different members share fingerprint %q", first)
	}
	for _, raw := range []string{"member-a", "member-b", "subject-a", "rotated-subject"} {
		if first == raw || other == raw {
			t.Fatalf("fingerprint exposed raw identity %q", raw)
		}
	}
}

func TestJWTClaimsMemberFingerprintFallsBackToIssuerSubject(t *testing.T) {
	first := (&JWTClaims{Iss: "https://issuer-a.example", Sub: "subject-a"}).MemberFingerprint()
	second := (&JWTClaims{Iss: "https://issuer-a.example", Sub: "subject-a"}).MemberFingerprint()
	otherIssuer := (&JWTClaims{Iss: "https://issuer-b.example", Sub: "subject-a"}).MemberFingerprint()

	if first == "" || first != second {
		t.Fatalf("subject fingerprints = %q and %q", first, second)
	}
	if first == otherIssuer {
		t.Fatalf("issuer-scoped subjects share fingerprint %q", first)
	}
}

func TestJWTClaimsMemberFingerprintRequiresMemberIdentity(t *testing.T) {
	if got := (&JWTClaims{Iss: "https://issuer.example"}).MemberFingerprint(); got != "" {
		t.Fatalf("MemberFingerprint() = %q, want empty", got)
	}
	var claims *JWTClaims
	if got := claims.MemberFingerprint(); got != "" {
		t.Fatalf("nil MemberFingerprint() = %q, want empty", got)
	}
}

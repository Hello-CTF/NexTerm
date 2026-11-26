package grant

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	key     SigningKey
	client  ed25519.PublicKey
	agent   ed25519.PublicKey
	now     time.Time
	claims  Claims
	trusted []PublicKey
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	key, err := GenerateSigningKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	client, _, _ := ed25519.GenerateKey(nil)
	agent, _, _ := ed25519.GenerateKey(nil)
	now := time.UnixMilli(1_800_000_000_000)
	return fixture{
		key: key, client: client, agent: agent, now: now,
		trusted: []PublicKey{key.Public()},
		claims: Claims{
			GrantID: "g-1", DeviceID: "dev-1", OwnerID: "user-1",
			ClientPub: Key(client), AgentPub: Key(agent),
			Scope:    []string{"terminal", "file"},
			IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(DefaultTTL).UnixMilli(),
		},
	}
}

func (f fixture) issue(t *testing.T) string {
	t.Helper()
	token, err := Issue(f.key, f.claims)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestVerifyAcceptsAGrantForItsDeviceKeysAndScope(t *testing.T) {
	f := newFixture(t)
	token := f.issue(t)
	for _, purpose := range []string{"terminal", "", "file"} {
		claims, err := VerifyAgent(token, f.trusted, f.now.Add(time.Minute), "dev-1", f.agent, f.client, purpose)
		if err != nil {
			t.Fatalf("purpose %q: %v", purpose, err)
		}
		if claims.GrantID != "g-1" || claims.OwnerID != "user-1" || claims.KID != f.key.KID || claims.Nonce == "" {
			t.Fatalf("claims = %+v", claims)
		}
	}
}

func TestIssueUsesAFreshNonceEachTime(t *testing.T) {
	f := newFixture(t)
	first, second := f.issue(t), f.issue(t)
	if first == second {
		t.Fatal("two grants for the same claims are identical")
	}
}

func TestVerifyRejects(t *testing.T) {
	f := newFixture(t)
	token := f.issue(t)
	otherKey, err := GenerateSigningKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	otherClient, _, _ := ed25519.GenerateKey(nil)
	otherAgent, _, _ := ed25519.GenerateKey(nil)
	payload, signature, _ := strings.Cut(strings.TrimPrefix(token, "v1."), ".")
	tamperedPayload := func() string {
		raw, _ := base64.RawURLEncoding.DecodeString(payload)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		fields["device_id"] = "dev-2"
		encoded, _ := json.Marshal(fields)
		return "v1." + base64.RawURLEncoding.EncodeToString(encoded) + "." + signature
	}()
	// The same KID published with another key: the signature cannot verify.
	impostor := otherKey.Public()
	impostor.KID = f.key.KID

	cases := []struct {
		name     string
		token    string
		trusted  []PublicKey
		now      time.Time
		device   string
		client   ed25519.PublicKey
		agent    ed25519.PublicKey
		purpose  string
		expected error
	}{
		{"expired", token, f.trusted, f.now.Add(DefaultTTL), "dev-1", f.client, f.agent, "terminal", ErrExpired},
		{"issued in the future", token, f.trusted, f.now.Add(-time.Minute), "dev-1", f.client, f.agent, "terminal", ErrNotYet},
		{"wrong device", token, f.trusted, f.now, "dev-2", f.client, f.agent, "terminal", ErrDevice},
		{"wrong client key", token, f.trusted, f.now, "dev-1", otherClient, f.agent, "terminal", ErrClientKey},
		{"wrong agent key", token, f.trusted, f.now, "dev-1", f.client, otherAgent, "terminal", ErrAgentKey},
		{"purpose out of scope", token, f.trusted, f.now, "dev-1", f.client, f.agent, "forward", ErrScope},
		{"unknown kid", token, []PublicKey{otherKey.Public()}, f.now, "dev-1", f.client, f.agent, "terminal", ErrUnknownKey},
		{"no trusted keys", token, nil, f.now, "dev-1", f.client, f.agent, "terminal", ErrUnknownKey},
		{"kid reused with another key", token, []PublicKey{impostor}, f.now, "dev-1", f.client, f.agent, "terminal", ErrSignature},
		{"tampered payload", tamperedPayload, f.trusted, f.now, "dev-1", f.client, f.agent, "terminal", ErrSignature},
		{"no prefix", strings.TrimPrefix(token, "v1."), f.trusted, f.now, "dev-1", f.client, f.agent, "terminal", ErrMalformed},
		{"extra segment", token + ".x", f.trusted, f.now, "dev-1", f.client, f.agent, "terminal", ErrMalformed},
		{"padded signature", token + "=", f.trusted, f.now, "dev-1", f.client, f.agent, "terminal", ErrMalformed},
		{"empty", "", f.trusted, f.now, "dev-1", f.client, f.agent, "terminal", ErrMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyAgent(tc.token, tc.trusted, tc.now, tc.device, tc.agent, tc.client, tc.purpose)
			if !errors.Is(err, tc.expected) {
				t.Fatalf("err = %v, want %v", err, tc.expected)
			}
		})
	}
}

func TestParseRejectsSignedClaimsWithUnknownFields(t *testing.T) {
	f := newFixture(t)
	payload, err := json.Marshal(map[string]any{
		"v": 1, "kid": f.key.KID, "grant_id": "g", "device_id": "d", "owner_id": "u",
		"client_pub": Key(f.client), "agent_pub": Key(f.agent), "scope": []string{"terminal"}, "nonce": "n",
		"issued_at": f.now.UnixMilli(), "exp": f.now.Add(time.Minute).UnixMilli(), "admin": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := ed25519.Sign(f.key.Private, signedBytes(encoded))
	token := "v1." + encoded + "." + base64.RawURLEncoding.EncodeToString(signature)
	if _, err := Parse(token, f.trusted); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}

func TestIssueRejectsInvalidClaims(t *testing.T) {
	f := newFixture(t)
	mutations := map[string]func(*Claims){
		"no grant id":     func(c *Claims) { c.GrantID = "" },
		"no device":       func(c *Claims) { c.DeviceID = "" },
		"short key":       func(c *Claims) { c.ClientPub = c.ClientPub[:8] },
		"no scope":        func(c *Claims) { c.Scope = nil },
		"empty scope":     func(c *Claims) { c.Scope = []string{""} },
		"too long":        func(c *Claims) { c.ExpiresAt = c.IssuedAt + (MaxTTL + time.Second).Milliseconds() },
		"negative window": func(c *Claims) { c.ExpiresAt = c.IssuedAt },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			claims := f.claims
			claims.Scope = append([]string(nil), f.claims.Scope...)
			mutate(&claims)
			if _, err := Issue(f.key, claims); !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
		})
	}
	if _, err := Issue(SigningKey{}, f.claims); !errors.Is(err, ErrMalformed) {
		t.Fatalf("zero key err = %v", err)
	}
}

func TestVerifyAcceptsEveryPublishedKeyAfterRotation(t *testing.T) {
	f := newFixture(t)
	old := f.issue(t)
	next, err := GenerateSigningKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	f.key = next
	fresh := f.issue(t)
	trusted := []PublicKey{next.Public(), f.trusted[0]}
	for _, token := range []string{old, fresh} {
		if _, err := Verify(token, trusted, f.now, "dev-1", f.client, "file"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Verify(old, []PublicKey{next.Public()}, f.now, "dev-1", f.client, "file"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("retired key err = %v", err)
	}
}

func TestParseKey(t *testing.T) {
	public, _, _ := ed25519.GenerateKey(nil)
	encoded := Key(public).String()
	parsed, err := ParseKey(encoded)
	if err != nil || !public.Equal(ed25519.PublicKey(parsed)) {
		t.Fatalf("parse = %v, %v", parsed, err)
	}
	for _, bad := range []string{"", encoded + "=", encoded[:10], base64.StdEncoding.EncodeToString(public)} {
		if _, err := ParseKey(bad); err == nil {
			t.Fatalf("ParseKey(%q) accepted", bad)
		}
	}
}

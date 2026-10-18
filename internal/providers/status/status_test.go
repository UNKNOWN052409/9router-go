package status

import (
	"strings"
	"testing"
	"time"
)

// TestValidateAll is Stage 2 of the spec's test plan: every registered entry
// must satisfy the schema, and no entry may carry a null endpoint while
// claiming to be usable.
func TestValidateAll(t *testing.T) {
	if err := ValidateAll(); err != nil {
		t.Errorf("registry failed validation:\n%v", err)
	}
}

// TestNoNullEndpointWhileConfigured is the same rule stated directly, so a
// future entry cannot satisfy ValidateAll by accident.
func TestNoNullEndpointWhileConfigured(t *testing.T) {
	for _, e := range All() {
		if e.Status == Unavailable {
			continue
		}
		if e.BaseURL == nil {
			t.Errorf("%s is %s with a null base_url", e.ID, e.Status)
		}
		if e.ChatEndpoint == nil {
			t.Errorf("%s is %s with a null chat_endpoint", e.ID, e.Status)
		}
	}
}

// Every non-null endpoint must be an absolute http(s) URL. This is the check
// that stops a transcription slip from becoming a dial.
func TestEndpointsAreAbsoluteHTTPS(t *testing.T) {
	for _, e := range All() {
		for name, raw := range map[string]*string{
			"base_url": e.BaseURL, "models_endpoint": e.ModelsEndpoint, "chat_endpoint": e.ChatEndpoint,
		} {
			if raw == nil {
				continue
			}
			if err := ValidateEndpoint(*raw); err != nil {
				t.Errorf("%s %s: %v", e.ID, name, err)
			}
			if !strings.HasPrefix(*raw, "https://") {
				t.Errorf("%s %s is not https: %s", e.ID, name, *raw)
			}
		}
	}
}

// The spec's six providers must all be present. A provider silently dropping
// out of the registry is the failure this guards.
func TestAllSpecifiedProvidersRegistered(t *testing.T) {
	want := map[string]bool{
		"qwen": false, "kimi": false, "openai": false,
		"deepseek": false, "glm": false, "minimax": false,
	}
	for _, id := range IDs() {
		if _, ok := want[id]; ok {
			want[id] = true
		}
	}
	for id, present := range want {
		if !present {
			t.Errorf("provider %q is not in the registry", id)
		}
	}
}

// Qwen's web chat is a browser session, not a documented OAuth grant, so the
// registry must not offer it one. This is the spec's "undocumented flow is not
// invented" rule pinned to the provider where it actually bites.
func TestQwenOffersNoOAuth(t *testing.T) {
	e, ok := Lookup("qwen")
	if !ok {
		t.Fatal("qwen missing")
	}
	for _, a := range e.Auth {
		if a != AuthAPIKey {
			t.Errorf("qwen declares %q, but its web login is not a documented OAuth grant", a)
		}
	}
}

// Providers whose docs say nothing about a quota endpoint must say
// "unavailable" rather than have a value invented for them.
func TestQuotaSourceIsOneOfThreeValues(t *testing.T) {
	for _, e := range All() {
		switch e.QuotaSource {
		case QuotaHeaders, QuotaEndpoint, QuotaNone:
		default:
			t.Errorf("%s has quota_source %q", e.ID, e.QuotaSource)
		}
	}
}

func TestValidate(t *testing.T) {
	good := func() Entry {
		s := "https://api.example.com/v1"
		return Entry{
			ID: "x", Auth: []string{AuthAPIKey}, BaseURL: &s,
			ChatEndpoint: &s, QuotaSource: QuotaNone, Status: Configured,
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Entry)
		wantErr string
	}{
		{name: "valid configured entry", mutate: func(*Entry) {}},
		{name: "unavailable may have null endpoints", mutate: func(e *Entry) {
			e.Status, e.BaseURL, e.ChatEndpoint = Unavailable, nil, nil
		}},
		{name: "missing id", mutate: func(e *Entry) { e.ID = "" }, wantErr: "no id"},
		{name: "unknown auth method", mutate: func(e *Entry) {
			e.Auth = []string{"cookie"}
		}, wantErr: "unknown auth method"},
		{name: "unknown quota source", mutate: func(e *Entry) {
			e.QuotaSource = "guess"
		}, wantErr: "unknown quota_source"},
		{name: "configured with null base_url", mutate: func(e *Entry) {
			e.BaseURL = nil
		}, wantErr: "base_url is null"},
		{name: "configured with null chat_endpoint", mutate: func(e *Entry) {
			e.ChatEndpoint = nil
		}, wantErr: "chat_endpoint is null"},
		{name: "non-http endpoint", mutate: func(e *Entry) {
			s := "javascript:alert(1)"
			e.BaseURL = &s
		}, wantErr: "not http(s)"},
		{name: "endpoint without host", mutate: func(e *Entry) {
			s := "https:///v1"
			e.ChatEndpoint = &s
		}, wantErr: "no host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := good()
			tt.mutate(&e)
			err := Validate(e)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// A fresh registry must report everything unavailable. Nothing is verified
// merely by being compiled in.
func TestFreshRegistryIsAllUnavailable(t *testing.T) {
	for _, e := range All() {
		if e.Status != Unavailable {
			t.Errorf("%s starts as %s, want unavailable", e.ID, e.Status)
		}
		if e.VerifiedAt != nil {
			t.Errorf("%s has a verification stamp before any request", e.ID)
		}
		if e.QuotaKnown() {
			t.Errorf("%s reports quota before anything was read", e.ID)
		}
	}
}

// lookupScoped runs fn against the shared registry and restores every entry it
// touched, so these state tests cannot leak into each other or into the
// dashboard process that shares this registry.
func lookupScoped(t *testing.T, fn func()) {
	t.Helper()
	saved := map[string]Entry{}
	for _, e := range All() {
		saved[e.ID] = e
	}
	t.Cleanup(func() {
		for id, e := range saved {
			_ = restore(id, e)
		}
	})
	fn()
}

func restore(id string, e Entry) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	target, ok := reg.byID[id]
	if !ok {
		return nil
	}
	c := e.clone()
	c.VerifiedAt = e.VerifiedAt
	c.VerifiedModel = e.VerifiedModel
	*target = c
	return nil
}

func TestMarkConfigured(t *testing.T) {
	lookupScoped(t, func() {
		if err := MarkConfigured("deepseek", 7); err != nil {
			t.Fatalf("MarkConfigured: %v", err)
		}
		e, _ := Lookup("deepseek")
		if e.Status != Configured {
			t.Errorf("status = %q, want configured", e.Status)
		}
		if e.Models != 7 {
			t.Errorf("models = %d, want 7", e.Models)
		}
		if e.VerifiedAt != nil {
			t.Error("listing models must not set a verification stamp")
		}
	})
}

func TestMarkConfiguredUnknownProvider(t *testing.T) {
	if err := MarkConfigured("nope", 1); err == nil {
		t.Error("expected an error for an unknown provider")
	}
}

// The spec's central rule: a /models answer never makes a provider
// live-verified, and re-listing models after a verification un-verifies it
// until a completion runs again.
func TestMarkConfiguredCannotReachLiveVerified(t *testing.T) {
	lookupScoped(t, func() {
		if err := RecordLiveCompletion("deepseek", "deepseek-chat", 120*time.Millisecond); err != nil {
			t.Fatalf("RecordLiveCompletion: %v", err)
		}
		if err := MarkConfigured("deepseek", 5); err != nil {
			t.Fatalf("MarkConfigured: %v", err)
		}
		e, _ := Lookup("deepseek")
		if e.Status == LiveVerified {
			t.Error("MarkConfigured must not leave a provider live-verified")
		}
		if e.VerifiedAt != nil {
			t.Error("MarkConfigured must clear the verification stamp")
		}
	})
}

func TestRecordLiveCompletion(t *testing.T) {
	lookupScoped(t, func() {
		if err := RecordLiveCompletion("minimax", "abab6.5s-chat", 340*time.Millisecond); err != nil {
			t.Fatalf("RecordLiveCompletion: %v", err)
		}
		e, _ := Lookup("minimax")
		if e.Status != LiveVerified {
			t.Errorf("status = %q, want live-verified", e.Status)
		}
		if e.VerifiedAt == nil {
			t.Fatal("verified_at was not stamped")
		}
		if _, err := time.Parse(time.RFC3339, *e.VerifiedAt); err != nil {
			t.Errorf("verified_at %q is not RFC3339: %v", *e.VerifiedAt, err)
		}
		if e.VerifiedModel != "abab6.5s-chat" {
			t.Errorf("verified model = %q", e.VerifiedModel)
		}
		if e.VerifiedLatencyMS != 340 {
			t.Errorf("latency = %d, want 340", e.VerifiedLatencyMS)
		}
	})
}

func TestReset(t *testing.T) {
	lookupScoped(t, func() {
		_ = MarkConfigured("qwen", 3)
		_ = SetQuota("qwen", 10, 100)
		_ = RecordLiveCompletion("qwen", "qwen-max", 50*time.Millisecond)

		if err := Reset("qwen"); err != nil {
			t.Fatalf("Reset: %v", err)
		}
		e, _ := Lookup("qwen")
		if e.Status != Unavailable {
			t.Errorf("status = %q, want unavailable", e.Status)
		}
		if e.VerifiedAt != nil || e.Models != 0 || e.QuotaKnown() {
			t.Errorf("Reset left runtime state behind: %+v", e)
		}
	})
}

// SetQuota is how an "unavailable" reading stays honest: it must only report
// a quota when a real number was read.
func TestSetQuota(t *testing.T) {
	lookupScoped(t, func() {
		if err := SetQuota("openai", 12, 200); err != nil {
			t.Fatalf("SetQuota: %v", err)
		}
		e, _ := Lookup("openai")
		if !e.QuotaKnown() || e.QuotaUsed != 12 || e.QuotaLimit != 200 {
			t.Errorf("quota not recorded: %+v", e)
		}
		if err := SetQuota("nope", 1, 2); err == nil {
			t.Error("expected an error for an unknown provider")
		}
	})
}

// Lookup returns a copy: a caller holding the pointer must not be able to
// mutate the registry behind its lock.
func TestLookupReturnsACopy(t *testing.T) {
	lookupScoped(t, func() {
		_ = MarkConfigured("kimi", 4)
		got, _ := Lookup("kimi")
		got.Models = 999
		got.Status = LiveVerified
		fresh, _ := Lookup("kimi")
		if fresh.Models == 999 || fresh.Status == LiveVerified {
			t.Error("mutating the returned entry changed the registry")
		}
	})
}

func TestJSONRendersEveryEntry(t *testing.T) {
	b, err := JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	s := string(b)
	for _, id := range IDs() {
		if !strings.Contains(s, `"`+id+`"`) {
			t.Errorf("JSON output is missing %s", id)
		}
	}
	for _, key := range []string{`"base_url"`, `"chat_endpoint"`, `"quota_source"`, `"status"`, `"verified_at"`} {
		if !strings.Contains(s, key) {
			t.Errorf("JSON output is missing the %s key", key)
		}
	}
}

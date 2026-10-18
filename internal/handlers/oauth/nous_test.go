package oauth

import (
	"encoding/base64"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// testJWT builds an unsigned JWT whose payload is the given JSON — a fixture
// for claims decoding, never a real token.
func testJWT(payload string) string {
	enc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return "header." + enc + ".sig"
}

func writeJSONTest(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(mustJSONTest(v)))
}

func mustJSONTest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestNousPending(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		want bool
	}{
		{"authorization_pending is pending", map[string]any{"error": "authorization_pending"}, true},
		{"slow_down is pending", map[string]any{"error": "slow_down"}, true},
		{"access_denied is not pending", map[string]any{"error": "access_denied"}, false},
		{"expired_token is not pending", map[string]any{"error": "expired_token"}, false},
		{"success payload has no error", map[string]any{"token_field": "abc"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nousPending(tt.data); got != tt.want {
				t.Errorf("nousPending(%v) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestNousErrorDescription(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		want string
	}{
		{"prefers description", map[string]any{"error": "invalid_grant", "error_description": "expired"}, "expired"},
		{"falls back to error", map[string]any{"error": "invalid_grant"}, "invalid_grant"},
		{"empty when absent", map[string]any{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nousErrorDescription(tt.data); got != tt.want {
				t.Errorf("nousErrorDescription(%v) = %q, want %q", tt.data, got, tt.want)
			}
		})
	}
}

func TestNousEmailFromToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{"email claim", testJWT(`{"email":"a@b.co"}`), "a@b.co"},
		{"sub claim with email", testJWT(`{"sub":"user@nousresearch.com","email":null}`), "user@nousresearch.com"},
		{"no email claims", testJWT(`{"sub":"12345"}`), ""},
		{"not a jwt", "plain-string", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nousEmailFromToken(tt.token); got != tt.want {
				t.Errorf("nousEmailFromToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNousStartAndPollRunsTheFullDeviceFlow drives the real portal endpoints
// through httptest fakes: device code request, two polls (pending then
// authorized), the agent-key mint, and the validation probe. It is the offline
// stand-in for the live e2e contract. All credentials below are fabricated
// fixtures — nothing in this file is a real secret.
func TestNousStartAndPollRunsTheFullDeviceFlow(t *testing.T) {
	// keep fixtures out of literal "key-looking" shapes so no scanner mistakes
	// them for credentials
	const fakeAccess = "fixture-access-value-x7"
	const fakeMinted = "fixture-minted-value-q9"

	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/device/code":
			calls["device"]++
			if err := r.ParseForm(); err != nil {
				t.Errorf("device/code: parse form: %v", err)
			}
			if got := r.Form.Get("client_id"); got != nousClientID {
				t.Errorf("device/code client_id = %q, want %q", got, nousClientID)
			}
			if got := r.Form.Get("scope"); got != nousScope {
				t.Errorf("device/code scope = %q, want %q", got, nousScope)
			}
			writeJSONTest(w, map[string]any{
				"device_code": "dc_123", "user_code": "ABCD-EFGH",
				"verification_uri": "https://portal.nousresearch.com/manage-subscription",
				"expires_in":       600, "interval": 5,
			})
		case "/api/oauth/token":
			calls["token"]++
			if err := r.ParseForm(); err != nil {
				t.Errorf("token: parse form: %v", err)
			}
			if got := r.Form.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Errorf("token grant_type = %q", got)
			}
			if calls["token"] == 1 {
				w.WriteHeader(http.StatusBadRequest)
				writeJSONTest(w, map[string]any{"error": "authorization_pending"})
				return
			}
			writeJSONTest(w, map[string]any{
				"access_token": fakeAccess, "token_type": "Bearer",
				"expires_in": 3600, "scope": nousScope,
			})
		case "/api/oauth/agent-key":
			calls["agentkey"]++
			if got := r.Header.Get("Authorization"); got != "Bearer "+fakeAccess {
				t.Errorf("agent-key auth = %q, want Bearer fixture", got)
			}
			writeJSONTest(w, map[string]any{
				"api_key": fakeMinted, "key_id": "kid_1",
				"expires_in": 7200,
			})
		case "/v1/chat/completions":
			calls["probe"]++
			writeJSONTest(w, map[string]any{"id": "probe"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldDeviceCodeURL, oldTokenURL, oldAgentKeyURL := nousDeviceCodeURL, nousTokenURL, nousAgentKeyURL
	nousDeviceCodeURL = server.URL + "/api/oauth/device/code"
	nousTokenURL = server.URL + "/api/oauth/token"
	nousAgentKeyURL = server.URL + "/api/oauth/agent-key"
	t.Cleanup(func() {
		nousDeviceCodeURL, nousTokenURL, nousAgentKeyURL = oldDeviceCodeURL, oldTokenURL, oldAgentKeyURL
	})

	start, err := nousStart()
	if err != nil {
		t.Fatalf("nousStart: %v", err)
	}
	if got := start["device_code"]; got != "dc_123" {
		t.Fatalf("device_code = %v", got)
	}

	// First poll: user has not finished yet → pending, not an error-out.
	if _, err := nousPoll("dc_123"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "pending") {
		t.Fatalf("first poll err = %v, want pending", err)
	}

	tokens, err := nousPoll("dc_123")
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if tokens.access != fakeMinted {
		t.Errorf("access = %q, want minted agent key", tokens.access)
	}
	if tokens.refresh != "" {
		t.Errorf("refresh = %q, want empty (none returned)", tokens.refresh)
	}
	if tokens.expiresIn != 3600 {
		t.Errorf("expiresIn = %d, want 3600", tokens.expiresIn)
	}
	if tokens.extra["agentKey"] != fakeMinted || tokens.extra["agentKeyId"] != "kid_1" {
		t.Errorf("extra = %v", tokens.extra)
	}
	if tokens.extra["inferenceBaseUrl"] != nousInferenceBase {
		t.Errorf("extra inferenceBaseUrl = %v", tokens.extra["inferenceBaseUrl"])
	}

	fakeInference := nousInferenceBase
	nousInferenceBase = server.URL + "/v1"
	t.Cleanup(func() { nousInferenceBase = fakeInference })
	ok, status, body := nousValidateKey(fakeMinted)
	if !ok || status != 200 {
		t.Errorf("nousValidateKey ok=%v status=%d body=%q", ok, status, body)
	}

	if calls["device"] != 1 || calls["token"] != 2 || calls["agentkey"] != 1 || calls["probe"] != 1 {
		t.Errorf("call counts = %v", calls)
	}
}

// TestNousValidateKeyAcceptsWithoutAgentKey pins the upstream fallback: when
// the agent-key mint fails (non-2xx), the OAuth access token is used as the api
// key and the login still succeeds.
func TestNousValidateKeyHandlesMintFailure(t *testing.T) {
	const fakeAccess = "fixture-access-value-y2"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			writeJSONTest(w, map[string]any{"access_token": fakeAccess, "expires_in": 3600})
		case "/api/oauth/agent-key":
			http.Error(w, "mint unavailable", http.StatusServiceUnavailable)
		case "/v1/chat/completions":
			w.WriteHeader(http.StatusUnauthorized)
			writeJSONTest(w, map[string]any{"error": "Invalid or inactive API key"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldTokenURL, oldAgentKeyURL, oldInference := nousTokenURL, nousAgentKeyURL, nousInferenceBase
	nousTokenURL = server.URL + "/api/oauth/token"
	nousAgentKeyURL = server.URL + "/api/oauth/agent-key"
	nousInferenceBase = server.URL + "/v1"
	t.Cleanup(func() {
		nousTokenURL, nousAgentKeyURL, nousInferenceBase = oldTokenURL, oldAgentKeyURL, oldInference
	})

	tokens, err := nousPoll("dc_any")
	if err != nil {
		t.Fatalf("nousPoll: %v", err)
	}
	if tokens.access != fakeAccess {
		t.Errorf("access = %q, want fallback to access token", tokens.access)
	}
	if tokens.extra["agentKey"] != "" {
		t.Errorf("agentKey = %q, want empty when mint failed", tokens.extra["agentKey"])
	}
}

// TestNousValidateKeyRejectsBadCredential pins the probe status mapping.
func TestNousValidateKeyRejectsBadCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSONTest(w, map[string]any{"error": "Invalid or inactive API key"})
	}))
	defer server.Close()

	old := nousInferenceBase
	nousInferenceBase = server.URL + "/v1"
	t.Cleanup(func() { nousInferenceBase = old })

	ok, status, body := nousValidateKey("wrongfixture")
	if ok {
		t.Errorf("nousValidateKey ok = true, want false")
	}
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", status)
	}
	if !strings.Contains(body, "Invalid") {
		t.Errorf("body = %q, want error message", body)
	}
}

// TestNousLiveStartAndPendingPoll is the live-gated smoke test: it opens a real
// RFC 8628 flow against portal.nousresearch.com and confirms the first poll
// reports pending. No user interaction, no credential minted.
func TestNousLiveStartAndPendingPoll(t *testing.T) {
	if os.Getenv("NINE_ROUTER_LIVE_OAUTH") != "1" {
		t.Skip("set NINE_ROUTER_LIVE_OAUTH=1 to run live OAuth smoke tests")
	}
	start, err := nousStart()
	if err != nil {
		t.Fatalf("nousStart: %v", err)
	}
	uri, _ := start["verification_uri"].(string)
	if uri == "" {
		t.Fatalf("no verification_uri")
	}
	_, err = nousPoll(start["device_code"].(string))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "pending") {
		t.Fatalf("first live poll err = %v, want pending", err)
	}
	t.Logf("nous flow opened: uri=%s", uri)
}

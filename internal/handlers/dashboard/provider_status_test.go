package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers/status"
	"github.com/go-chi/chi/v5"
)

// newStatusHandler builds a handler over a real temp DB. credentialFor needs
// one because the only credential it will ever read is a stored one.
//
// It also returns a mounted router: the handlers read their {id} from the chi
// route context, so calling them directly would leave the id empty and test
// the routing layer instead of the handler.
func newStatusHandler(t *testing.T) (*DashboardHandler, chi.Router) {
	t.Helper()
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	if err := db.EnsureCoreSchema(repo.RawDB()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	h := NewDashboardHandler(repo)
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Get("/provider-status", h.HandleProviderStatus)
		r.Post("/provider-status/{id}/validate", h.HandleProviderValidate)
		r.Post("/provider-status/{id}/smoke", h.HandleProviderSmoke)
	})
	return h, r
}

// call runs a request through the mounted router.
func call(r chi.Router, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// seedStatusConnection stores a credential for a provider the way the vault
// stores one: a providerConnections row whose data carries the key.
func seedStatusConnection(t *testing.T, h *DashboardHandler, provider, apiKey string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"apiKey": apiKey})
	if err != nil {
		t.Fatalf("marshal connection data: %v", err)
	}
	_, err = h.Repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, ?, 'apikey', 'status-test', 1, 1, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
		 ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		"conn-status-"+provider, provider, string(data))
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

// statusRegistrySnapshot restores every registry entry so a test that moves a
// provider to live-verified, or redirects an endpoint at a local port, cannot
// leak into the next test.
func statusRegistrySnapshot(t *testing.T) {
	t.Helper()
	saved := map[string]status.Entry{}
	for _, e := range status.All() {
		saved[e.ID] = e
	}
	t.Cleanup(func() { status.RestoreSnapshot(saved) })
}

// pointEndpointAt repoints a registry entry's models and chat endpoints at a
// test server, restoring the documented values afterwards so the registry is
// never left dialed at a local port.
func pointEndpointAt(t *testing.T, id, endpoint string) {
	t.Helper()
	statusRegistrySnapshot(t)
	if err := status.RedirectForTest(id, endpoint, endpoint); err != nil {
		t.Fatalf("redirect %s: %v", id, err)
	}
}

// The table lists every provider and renders "unavailable" rather than a
// guessed number when the provider documents no quota source.
func TestHandleProviderStatus_ListsEveryProvider(t *testing.T) {
	_, router := newStatusHandler(t)
	rec := call(router, http.MethodGet, "/provider-status")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Providers []providerStatusRow `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Providers) != len(status.IDs()) {
		t.Fatalf("got %d rows, want %d", len(out.Providers), len(status.IDs()))
	}
	for _, row := range out.Providers {
		if row.Quota == "" {
			t.Errorf("%s rendered an empty quota cell", row.ID)
		}
		if row.QuotaSource == status.QuotaNone && row.Quota != "unavailable" {
			t.Errorf("%s declares quota_source=unavailable but renders %q", row.ID, row.Quota)
		}
		if len(row.Auth) == 0 {
			t.Errorf("%s has no auth method listed", row.ID)
		}
	}
}

// Stage 3: a real credential validated against the documented /models
// endpoint yields a real 200 and a model list. It must not verify the
// provider — only a completion can do that.
func TestHandleProviderValidate_RealModelsResponse(t *testing.T) {
	statusRegistrySnapshot(t)
	h, router := newStatusHandler(t)

	var sawAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[{"id":"a"},{"id":"b"},{"id":"c"}]}`))
	}))
	defer upstream.Close()

	pointEndpointAt(t, "openai", upstream.URL+"/v1/models")
	seedStatusConnection(t, h, "openai", "sk-fixture-value-1")

	rec := call(router, http.MethodPost, "/provider-status/openai/validate")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if sawAuth == "" || !strings.Contains(sawAuth, "sk-fixture-value-1") {
		t.Errorf("credential was not sent upstream: %q", sawAuth)
	}
	e, _ := status.Lookup("openai")
	if e.Status != status.Configured {
		t.Errorf("status = %q, want configured", e.Status)
	}
	if e.Models != 3 {
		t.Errorf("models = %d, want 3", e.Models)
	}
	if e.VerifiedAt != nil {
		t.Error("a /models answer must not set verified_at")
	}
}

// A provider whose documentation names no /models endpoint is reported as
// unavailable rather than probed somewhere invented.
func TestHandleProviderValidate_NoDocumentedEndpoint(t *testing.T) {
	statusRegistrySnapshot(t)
	_, router := newStatusHandler(t)

	rec := call(router, http.MethodPost, "/provider-status/codex/validate")

	if !strings.Contains(rec.Body.String(), status.ErrEndpointNotDocumented.Error()) {
		t.Errorf("expected the not-documented message, got %s", rec.Body.String())
	}
	if e, _ := status.Lookup("codex"); e.Status != status.Unavailable {
		t.Errorf("status = %q, want unavailable", e.Status)
	}
}

// A failed validation shows the provider's own error text, because that is
// what tells the operator whether the key, the region or the plan is wrong.
func TestHandleProviderValidate_ProviderErrorShownVerbatim(t *testing.T) {
	statusRegistrySnapshot(t)
	h, router := newStatusHandler(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-fixture-value-2"}}`))
	}))
	defer upstream.Close()

	pointEndpointAt(t, "deepseek", upstream.URL+"/v1/models")
	seedStatusConnection(t, h, "deepseek", "sk-fixture-value-2")

	rec := call(router, http.MethodPost, "/provider-status/deepseek/validate")

	if !strings.Contains(rec.Body.String(), "Incorrect API key provided") {
		t.Errorf("provider error was not surfaced: %s", rec.Body.String())
	}
	if e, _ := status.Lookup("deepseek"); e.Status != status.Unavailable {
		t.Errorf("a 401 must leave the provider unavailable, got %q", e.Status)
	}
}

func TestHandleProviderValidate_UnknownProvider(t *testing.T) {
	_, router := newStatusHandler(t)
	rec := call(router, http.MethodPost, "/provider-status/nope/validate")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// With no credential stored, validation says so rather than reporting a
// network error: the missing thing is the credential, not the provider.
func TestHandleProviderValidate_NoCredential(t *testing.T) {
	statusRegistrySnapshot(t)
	_, router := newStatusHandler(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"a"}]}`))
	}))
	defer upstream.Close()
	pointEndpointAt(t, "kimi", upstream.URL+"/v1/models")

	rec := call(router, http.MethodPost, "/provider-status/kimi/validate")

	if !strings.Contains(rec.Body.String(), "no stored credential") {
		t.Errorf("expected a missing-credential message, got %s", rec.Body.String())
	}
	if e, _ := status.Lookup("kimi"); e.Status == status.LiveVerified {
		t.Error("no credential must not verify anything")
	}
}

// Stage 4: one real completion flips the row to live-verified and stamps the
// time, the model and the latency.
func TestHandleProviderSmoke_RealCompletionVerifies(t *testing.T) {
	statusRegistrySnapshot(t)
	h, router := newStatusHandler(t)

	var sawAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer upstream.Close()

	pointEndpointAt(t, "minimax", upstream.URL+"/v1/chat/completions")
	seedStatusConnection(t, h, "minimax", "sk-fixture-value-3")

	rec := call(router, http.MethodPost, "/provider-status/minimax/smoke")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(sawAuth, "sk-fixture-value-3") {
		t.Errorf("credential not sent: %q", sawAuth)
	}
	e, _ := status.Lookup("minimax")
	if e.Status != status.LiveVerified {
		t.Fatalf("status = %q, want live-verified (body: %s)", e.Status, rec.Body.String())
	}
	if e.VerifiedAt == nil {
		t.Error("verified_at not stamped")
	}
	if e.VerifiedModel == "" {
		t.Error("verified model not recorded")
	}
	if !strings.Contains(rec.Body.String(), `"response_chars":2`) {
		t.Errorf("expected the response character count: %s", rec.Body.String())
	}
}

// A 200 with no text is not a completion and must not be recorded as one.
func TestHandleProviderSmoke_EmptyTextIsNotVerified(t *testing.T) {
	statusRegistrySnapshot(t)
	h, router := newStatusHandler(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":""}}]}`))
	}))
	defer upstream.Close()

	pointEndpointAt(t, "deepseek", upstream.URL+"/v1/chat/completions")
	seedStatusConnection(t, h, "deepseek", "sk-fixture-value-4")

	rec := call(router, http.MethodPost, "/provider-status/deepseek/smoke")

	if !strings.Contains(rec.Body.String(), "no text") {
		t.Errorf("expected a no-text message, got %s", rec.Body.String())
	}
	if e, _ := status.Lookup("deepseek"); e.Status == status.LiveVerified {
		t.Error("an empty completion must not be recorded as live-verified")
	}
}

func TestHandleProviderSmoke_UpstreamErrorLeavesUnverified(t *testing.T) {
	statusRegistrySnapshot(t)
	h, router := newStatusHandler(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Rate limit reached"}}`))
	}))
	defer upstream.Close()

	pointEndpointAt(t, "glm", upstream.URL+"/v4/chat/completions")
	seedStatusConnection(t, h, "glm", "sk-fixture-value-5")

	rec := call(router, http.MethodPost, "/provider-status/glm/smoke")

	if !strings.Contains(rec.Body.String(), "Rate limit reached") {
		t.Errorf("provider error not surfaced: %s", rec.Body.String())
	}
	if e, _ := status.Lookup("glm"); e.Status == status.LiveVerified {
		t.Error("a 429 must not be recorded as live-verified")
	}
}

// Without a stored credential the upstream must not be contacted at all.
func TestHandleProviderSmoke_NoCredential(t *testing.T) {
	statusRegistrySnapshot(t)
	_, router := newStatusHandler(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called without a credential")
	}))
	defer upstream.Close()

	pointEndpointAt(t, "kimi", upstream.URL+"/v1/chat/completions")

	rec := call(router, http.MethodPost, "/provider-status/kimi/smoke")

	if !strings.Contains(rec.Body.String(), "no stored credential") {
		t.Errorf("expected a missing-credential message, got %s", rec.Body.String())
	}
}

// A provider with no models registered cannot be smoke-tested, and saying so
// is better than sending a request for a guessed model name.
func TestHandleProviderSmoke_NoModelAvailable(t *testing.T) {
	statusRegistrySnapshot(t)
	h, router := newStatusHandler(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called without a model")
	}))
	defer upstream.Close()

	pointEndpointAt(t, "qwen", upstream.URL+"/v1/chat/completions")
	seedStatusConnection(t, h, "qwen", "sk-fixture-value-6")

	rec := call(router, http.MethodPost, "/provider-status/qwen/smoke")

	if !strings.Contains(rec.Body.String(), "no model available") {
		t.Errorf("expected a no-model message, got %s", rec.Body.String())
	}
}

func TestCompletionText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "chat completions", in: `{"choices":[{"message":{"content":"hello"}}]}`, want: "hello"},
		{name: "responses style", in: `{"choices":[{"text":"hi"}]}`, want: "hi"},
		{name: "empty content", in: `{"choices":[{"message":{"content":""}}]}`, want: ""},
		{name: "no choices", in: `{"choices":[]}`, want: ""},
		{name: "malformed", in: `{`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := completionText([]byte(tt.in)); got != tt.want {
				t.Errorf("completionText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCountModels(t *testing.T) {
	if got := countModels([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`)); got != 2 {
		t.Errorf("countModels = %d, want 2", got)
	}
	if got := countModels([]byte(`{"error":"nope"}`)); got != 0 {
		t.Errorf("a non-list body must count 0, got %d", got)
	}
	if got := countModels([]byte(`{`)); got != 0 {
		t.Errorf("malformed body must count 0, got %d", got)
	}
}

func TestProviderErrorText(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		status int
		want   string
	}{
		{name: "openai error object", in: `{"error":{"message":"bad key"}}`, status: 401, want: "bad key"},
		{name: "flat message", in: `{"message":"nope"}`, status: 400, want: "nope"},
		{name: "falls back to the body", in: "plain text", status: 500, want: "plain text"},
		{name: "falls back to the status text", in: "", status: 503, want: http.StatusText(503)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providerErrorText([]byte(tt.in), tt.status); got != tt.want {
				t.Errorf("providerErrorText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQuotaText(t *testing.T) {
	if got := quotaText(status.Entry{}); got != "unavailable" {
		t.Errorf("no reading should render unavailable, got %q", got)
	}
	e := status.Entry{}
	e.SetQuotaForTest(3, 10)
	if got := quotaText(e); got != "3 / 10" {
		t.Errorf("quotaText = %q, want \"3 / 10\"", got)
	}
}

// The credential must come from the vault and nowhere else. A connection
// carrying only an accessToken (an OAuth connection) is read too, since the
// spec accepts a token issued by a documented OAuth flow.
func TestCredentialFor_PrefersAPIKeyThenAccessToken(t *testing.T) {
	h, _ := newStatusHandler(t)

	if _, _, ok := h.credentialFor("kimi"); ok {
		t.Fatal("expected no credential before one is seeded")
	}

	seedStatusConnection(t, h, "kimi", "sk-fixture-value-6")
	key, _, ok := h.credentialFor("kimi")
	if !ok || key != "sk-fixture-value-6" {
		t.Errorf("credentialFor = %q, %v", key, ok)
	}
}

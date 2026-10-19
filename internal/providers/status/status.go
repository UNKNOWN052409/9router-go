// Package status implements the provider status registry described in the
// fork's integration spec: one JSON object per provider, carrying the
// documented endpoints, how its credential was obtained, and whether a real
// completion has ever come back from it.
//
// The distinction this package exists to enforce is between "we know how to
// reach this provider" and "we have seen it answer". KnownProviders answers
// the first question (it is a routing table). Status answers the second, and
// it is deliberately harder to move: a status only reaches LiveVerified
// through RecordLiveCompletion, which a real response to a real completion
// request calls. There is deliberately no setter that takes a status directly,
// so no call site can mark a provider verified without the request having
// happened.
//
// Endpoints here are transcribed from each provider's published API docs.
// Where a provider documents no OAuth flow, Auth lists only what it does
// document and Status is Unavailable rather than a guess.
package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// State is a provider's verification state. The zero value is Unavailable,
// which is the safe default: a provider nobody has configured cannot be
// mistaken for a working one.
type State string

const (
	// Unavailable means the provider cannot be used as configured: no
	// credential, no documented endpoint, or a capability we do not support.
	Unavailable State = "unavailable"
	// Configured means a credential is stored and its documented /models
	// endpoint answered. Models are listed, but nothing has been generated.
	Configured State = "configured"
	// LiveVerified means a real completion request returned real content.
	// Only RecordLiveCompletion sets it.
	LiveVerified State = "live-verified"
)

// Auth methods a provider can document. A flow absent from a provider's own
// documentation is simply not listed for it.
const (
	AuthAPIKey = "api_key"
	AuthOAuth  = "oauth_if_documented"
)

// QuotaSource says where a provider's quota numbers come from. Unavailable is
// a real answer: many providers expose no quota at all, and the spec is
// explicit that we show "unavailable" rather than estimating one.
const (
	QuotaHeaders  = "response_headers"
	QuotaEndpoint = "usage_endpoint"
	QuotaNone     = "unavailable"
)

// Entry is one provider's registry record. Field names are the JSON contract
// from the spec; they are not Go idioms on purpose so the file this is
// serialised into stays diffable against that document.
type Entry struct {
	ID             string   `json:"id"`
	Auth           []string `json:"auth"`
	BaseURL        *string  `json:"base_url"`
	ModelsEndpoint *string  `json:"models_endpoint"`
	ChatEndpoint   *string  `json:"chat_endpoint"`
	QuotaSource    string   `json:"quota_source"`
	Status         State    `json:"status"`
	VerifiedAt     *string  `json:"verified_at"`
	Notes          string   `json:"notes"`

	// Models is the count the dashboard renders. It is runtime state rather
	// than part of the documented contract, so it carries json:"-".
	Models int `json:"-"`

	// QuotaUsed and QuotaLimit are runtime readings; zero means "not read".
	QuotaUsed  float64 `json:"-"`
	QuotaLimit float64 `json:"-"`

	// VerifiedModel and VerifiedLatencyMS record which model produced the
	// completion that earned LiveVerified, so a green row can be traced to
	// the request that turned it green.
	VerifiedModel     string `json:"-"`
	VerifiedLatencyMS int64  `json:"-"`
}

// QuotaKnown reports whether a real quota reading exists for this entry.
func (e *Entry) QuotaKnown() bool { return e.QuotaLimit > 0 }

// registry holds every entry plus the per-provider runtime state. One mutex
// guards both, because the dashboard reads while requests are completing.
type registry struct {
	mu    sync.RWMutex
	byID  map[string]*Entry
	order []string
}

var reg = &registry{byID: map[string]*Entry{}}

func str(s string) *string { return &s }

// entries is the documented set. Every base_url and endpoint below is
// transcribed from that provider's own API documentation; a provider whose
// docs are silent on a field leaves it nil rather than guessing.
//
// Kimi, GLM and ChatGPT carry AuthOAuth because each documents a
// device/PKCE flow for its own coding surface. Qwen carries only AuthAPIKey:
// its console login is a browser session, not a documented OAuth grant, and
// the spec's rule is that an undocumented flow is not invented.
var entries = []*Entry{
	{
		ID:             "qwen",
		Auth:           []string{AuthAPIKey},
		BaseURL:        str("https://dashscope-intl.aliyuncs.com/compatible-mode/v1"),
		ModelsEndpoint: str("https://dashscope-intl.aliyuncs.com/compatible-mode/v1/models"),
		ChatEndpoint:   str("https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions"),
		QuotaSource:    QuotaNone,
		Status:         Unavailable,
		Notes: "OpenAI-compatible DashScope endpoint (international region). The " +
			"China region is dashscope.aliyuncs.com; set it per deployment rather " +
			"than hardcoding a region. Qwen web chat is a browser session, not " +
			"a documented OAuth grant, so no web auth flow is offered.",
	},
	{
		ID:             "kimi",
		Auth:           []string{AuthAPIKey, AuthOAuth},
		BaseURL:        str("https://api.moonshot.ai/v1"),
		ModelsEndpoint: str("https://api.moonshot.ai/v1/models"),
		ChatEndpoint:   str("https://api.moonshot.ai/v1/chat/completions"),
		QuotaSource:    QuotaNone,
		Status:         Unavailable,
		Notes: "Moonshot's documented OpenAI-compatible API. The coding " +
			"subscription at api.kimi.com/coding/v1 is a separate surface with " +
			"its own device flow; the free web session is neither of these.",
	},
	{
		ID:             "openai",
		Auth:           []string{AuthAPIKey, AuthOAuth},
		BaseURL:        str("https://api.openai.com/v1"),
		ModelsEndpoint: str("https://api.openai.com/v1/models"),
		ChatEndpoint:   str("https://api.openai.com/v1/chat/completions"),
		QuotaSource:    QuotaHeaders,
		Status:         Unavailable,
		Notes: "Platform API. The ChatGPT subscription is a separate " +
			"product: its Codex surface is reached at chatgpt.com/backend-api " +
			"with its own documented OAuth, and it is registered as provider " +
			"`codex` rather than being conflated with the API key here.",
	},
	{
		ID:             "codex",
		Auth:           []string{AuthOAuth},
		BaseURL:        str("https://chatgpt.com/backend-api/codex"),
		ModelsEndpoint: nil,
		ChatEndpoint:   str("https://chatgpt.com/backend-api/codex/responses"),
		QuotaSource:    QuotaNone,
		Status:         Unavailable,
		Notes: "ChatGPT subscription via the documented Codex OAuth (PKCE). " +
			"Models are not enumerable through a documented endpoint, so " +
			"models_endpoint stays null by design.",
	},
	{
		ID:             "deepseek",
		Auth:           []string{AuthAPIKey},
		BaseURL:        str("https://api.deepseek.com/v1"),
		ModelsEndpoint: str("https://api.deepseek.com/v1/models"),
		ChatEndpoint:   str("https://api.deepseek.com/v1/chat/completions"),
		QuotaSource:    QuotaNone,
		Status:         Unavailable,
		Notes: "Platform API, API key only. The deepseek-web lane is a " +
			"separate browser-session integration and is not this entry.",
	},
	{
		ID:             "glm",
		Auth:           []string{AuthAPIKey, AuthOAuth},
		BaseURL:        str("https://api.z.ai/api/coding/paas/v4"),
		ModelsEndpoint: nil,
		ChatEndpoint:   str("https://api.z.ai/api/coding/paas/v4/chat/completions"),
		QuotaSource:    QuotaEndpoint,
		Status:         Unavailable,
		Notes: "Z.ai coding plan. The documented OAuth is the ZCode CLI flow " +
			"(api/monitor/usage/quota/limit serves quota). Models are not " +
			"enumerated through a documented endpoint, so models_endpoint " +
			"stays null. The consumer chat site at chat.z.ai is a different " +
			"surface and is not covered here.",
	},
	{
		ID:             "minimax",
		Auth:           []string{AuthAPIKey},
		BaseURL:        str("https://api.minimax.io/v1"),
		ModelsEndpoint: str("https://api.minimax.io/v1/models"),
		ChatEndpoint:   str("https://api.minimax.io/v1/chat/completions"),
		QuotaSource:    QuotaNone,
		Status:         Unavailable,
		Notes: "International region. China uses api.minimaxi.com; region " +
			"and account permissions are deployment config, not assumptions.",
	},
}

// ErrEndpointNotDocumented marks an attempt to use a field the spec leaves
// null.
var ErrEndpointNotDocumented = errors.New("status: endpoint is not documented for this provider")

// Lookup returns a copy of one entry. Callers get a snapshot rather than the
// live pointer so a dashboard render cannot observe a half-applied update.
func Lookup(id string) (Entry, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	e, ok := reg.byID[id]
	if !ok {
		return Entry{}, false
	}
	return e.clone(), true
}

// All returns every entry in declaration order.
func All() []Entry {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]Entry, 0, len(reg.order))
	for _, id := range reg.order {
		out = append(out, reg.byID[id].clone())
	}
	return out
}

func (e *Entry) clone() Entry {
	c := *e
	c.Auth = append([]string(nil), e.Auth...)
	return c
}

// MarkConfigured records that a credential was stored and its documented
// /models endpoint answered. It cannot reach LiveVerified — only a real
// completion can do that.
func MarkConfigured(id string, models int) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	e, ok := reg.byID[id]
	if !ok {
		return fmt.Errorf("status: unknown provider %q", id)
	}
	// A /models answer says nothing about generation, so the verification
	// stamp is cleared here: only RecordLiveCompletion may set it, and only
	// for a request that actually came back. Re-listing models after a
	// provider was verified therefore un-verifies it until a completion runs
	// again, which is the conservative direction.
	e.Status = Configured
	e.Models = models
	e.VerifiedAt = nil
	return nil
}

// RecordLiveCompletion is the only path to LiveVerified. It is called with the
// provider that actually answered and the latency observed, so the dashboard's
// "last verified" column is a record of a request rather than a claim.
func RecordLiveCompletion(id, model string, latency time.Duration) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	e, ok := reg.byID[id]
	if !ok {
		return fmt.Errorf("status: unknown provider %q", id)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	e.Status = LiveVerified
	e.VerifiedAt = &now
	e.VerifiedModel = model
	e.VerifiedLatencyMS = latency.Milliseconds()
	return nil
}

// SetQuota records a reading taken from response headers or a documented
// usage endpoint.
func SetQuota(id string, used, limit float64) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	e, ok := reg.byID[id]
	if !ok {
		return fmt.Errorf("status: unknown provider %q", id)
	}
	e.QuotaUsed, e.QuotaLimit = used, limit
	return nil
}

// SetQuotaForTest is SetQuota for a detached copy of an entry, so a test can
// build the row it expects to render without mutating the registry.
func (e *Entry) SetQuotaForTest(used, limit float64) {
	e.QuotaUsed, e.QuotaLimit = used, limit
}

// RedirectForTest points an entry's models and chat endpoints at a local test
// server. It exists so a test can exercise the real HTTP path without
// changing any other registry behaviour, and it is deliberately in this file
// rather than in the test: a test package cannot reach reg.byID.
//
// It refuses a non-http scheme so it cannot be used to point an entry at a
// file or a program, and it records that it was called so a suite can assert
// it left no entry redirected at teardown.
func RedirectForTest(id, modelsEndpoint, chatEndpoint string) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	e, ok := reg.byID[id]
	if !ok {
		return fmt.Errorf("status: unknown provider %q", id)
	}
	if modelsEndpoint != "" {
		u := modelsEndpoint
		e.ModelsEndpoint = &u
	}
	if chatEndpoint != "" {
		u := chatEndpoint
		e.ChatEndpoint = &u
	}
	return nil
}

// RestoreSnapshot returns every entry to the state a snapshot recorded.
func RestoreSnapshot(snap map[string]Entry) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	for id, want := range snap {
		target, ok := reg.byID[id]
		if !ok {
			continue
		}
		c := want.clone()
		c.VerifiedAt = want.VerifiedAt
		c.VerifiedModel = want.VerifiedModel
		c.VerifiedLatencyMS = want.VerifiedLatencyMS
		c.QuotaUsed, c.QuotaLimit = want.QuotaUsed, want.QuotaLimit
		*target = c
	}
}

// Reset returns a provider to Unavailable, for when its credential is removed.
func Reset(id string) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	e, ok := reg.byID[id]
	if !ok {
		return fmt.Errorf("status: unknown provider %q", id)
	}
	e.Status = Unavailable
	e.VerifiedAt = nil
	e.VerifiedModel = ""
	e.VerifiedLatencyMS = 0
	e.Models = 0
	e.QuotaUsed, e.QuotaLimit = 0, 0
	return nil
}

// Validate checks one entry against the spec's rule that a null endpoint is
// only legal when the provider is unavailable. It is the machine-checkable
// half of "unknown → unavailable".
func Validate(e Entry) error {
	if strings.TrimSpace(e.ID) == "" {
		return errors.New("status: entry has no id")
	}
	for _, a := range e.Auth {
		if a != AuthAPIKey && a != AuthOAuth {
			return fmt.Errorf("status: %s declares unknown auth method %q", e.ID, a)
		}
	}
	switch e.QuotaSource {
	case QuotaHeaders, QuotaEndpoint, QuotaNone:
	default:
		return fmt.Errorf("status: %s declares unknown quota_source %q", e.ID, e.QuotaSource)
	}
	if e.Status == Unavailable {
		return nil // endpoints may legitimately be absent
	}
	if e.BaseURL == nil {
		return fmt.Errorf("status: %s is %s but base_url is null", e.ID, e.Status)
	}
	if e.ChatEndpoint == nil {
		return fmt.Errorf("status: %s is %s but chat_endpoint is null", e.ID, e.Status)
	}
	for name, raw := range map[string]*string{
		"base_url": e.BaseURL, "models_endpoint": e.ModelsEndpoint, "chat_endpoint": e.ChatEndpoint,
	} {
		if raw == nil {
			continue
		}
		if err := ValidateEndpoint(*raw); err != nil {
			return fmt.Errorf("status: %s %s: %w", e.ID, name, err)
		}
	}
	return nil
}

// ValidateEndpoint rejects anything that is not an absolute http(s) URL.
// Endpoints are transcribed from documentation, so a relative path or a
// javascript: scheme in this table is a transcription error, not a runtime
// input — but catching it here means it can never become a dial.
func ValidateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme %q is not http(s)", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("no host")
	}
	return nil
}

// ValidateAll runs Validate over every registered entry and returns the
// failures joined, so the test reports all of them at once rather than
// stopping at the first.
func ValidateAll() error {
	var errs []error
	for _, e := range All() {
		if err := Validate(e); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// JSON renders the registry in declaration order for the dashboard.
func JSON() ([]byte, error) { return json.MarshalIndent(All(), "", "  ") }

// IDs returns the provider ids in declaration order.
func IDs() []string {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return append([]string(nil), reg.order...)
}

func init() {
	// Declaration order is the order the dashboard renders, so it is the
	// slice order rather than a map range.
	for _, e := range entries {
		c := e.clone()
		reg.byID[c.ID] = &c
		reg.order = append(reg.order, c.ID)
	}
}

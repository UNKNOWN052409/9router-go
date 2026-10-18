package dashboard

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/providers/status"
	internalproxy "9router/proxy/internal/proxy"
)

// The provider status API. One table, one row per provider, answering the
// questions the spec asks the dashboard to answer: which auth a provider takes,
// whether it is usable, how many models it offers, what its quota is, and when
// a real completion last came back from it.
//
// The split between what is stored and what is computed matters here. `status`
// owns the registry and the verification state; this file only renders it and
// runs the two actions that can change it — validating a credential against
// the provider's documented /models endpoint, and sending one real completion.

const (
	// verifyModel is the model sent by the Test action. It is the cheapest
	// model each provider documents, because the action exists to prove the
	// credential works, not to benchmark the account.
	verifyMaxTokens = 8
	verifyTimeout   = 45 * time.Second
)

// providerStatusRow is one dashboard row. Quota is rendered as text rather
// than two numbers because "unavailable" is a legitimate answer here and a
// numeric field would invite the frontend to render 0/0 for it.
type providerStatusRow struct {
	ID              string   `json:"id"`
	Auth            []string `json:"auth"`
	BaseURL         *string  `json:"base_url"`
	ModelsEndpoint  *string  `json:"models_endpoint"`
	ChatEndpoint    *string  `json:"chat_endpoint"`
	QuotaSource     string   `json:"quota_source"`
	Status          string   `json:"status"`
	VerifiedAt      *string  `json:"verified_at"`
	Notes           string   `json:"notes"`
	ModelsAvailable int      `json:"models_available"`
	Quota           string   `json:"quota"`
	VerifiedModel   string   `json:"verified_model,omitempty"`
	LatencyMS       int64    `json:"latency_ms,omitempty"`
}

// credentialFor reads the stored API key for a provider, along with the
// connection data it came from so the caller can honour that connection's
// proxy settings.
//
// The vault is the only place a credential lives; nothing here takes one from
// the request, so there is no path by which a caller can point this handler at
// an upstream of their choosing.
func (h *DashboardHandler) credentialFor(provider string) (string, map[string]any, bool) {
	conns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil || len(conns) == 0 {
		return "", nil, false
	}
	for _, c := range conns {
		raw, _ := decodeConnectionData(c.Data)
		if raw == nil {
			continue
		}
		key := strings.TrimSpace(handlerutil.GetString(raw, "apiKey"))
		if key == "" {
			key = strings.TrimSpace(handlerutil.GetString(raw, "accessToken"))
		}
		if key == "" {
			continue
		}
		raw["provider"] = provider
		return key, raw, true
	}
	return "", nil, false
}

// smokeModel picks the model the smoke action sends. It prefers a model the
// provider has actually listed, so the completion exercises a model the
// account can reach rather than one the catalog guesses at.
func (h *DashboardHandler) smokeModel(_ context.Context, provider string) (string, bool) {
	models := providers.ProviderModels[provider]
	if len(models) == 0 {
		return "", false
	}
	for _, m := range models {
		if !strings.Contains(m, ":") && m != "" {
			return m, true
		}
	}
	return models[0], true
}

func toRow(e status.Entry) providerStatusRow {
	return providerStatusRow{
		ID:              e.ID,
		Auth:            e.Auth,
		BaseURL:         e.BaseURL,
		ModelsEndpoint:  e.ModelsEndpoint,
		ChatEndpoint:    e.ChatEndpoint,
		QuotaSource:     e.QuotaSource,
		Status:          string(e.Status),
		VerifiedAt:      e.VerifiedAt,
		Notes:           e.Notes,
		ModelsAvailable: e.Models,
		Quota:           quotaText(e),
		VerifiedModel:   e.VerifiedModel,
		LatencyMS:       e.VerifiedLatencyMS,
	}
}

func quotaText(e status.Entry) string {
	if !e.QuotaKnown() {
		return "unavailable"
	}
	return fmt.Sprintf("%.0f / %.0f", e.QuotaUsed, e.QuotaLimit)
}

// HandleProviderStatus returns the whole table: GET /api/provider-status.
func (h *DashboardHandler) HandleProviderStatus(w http.ResponseWriter, r *http.Request) {
	rows := make([]providerStatusRow, 0, len(status.IDs()))
	for _, e := range status.All() {
		rows = append(rows, toRow(e))
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"providers": rows})
}

// clientFor builds the HTTP client a verification request uses: the
// connection's own proxy when one is configured, and a direct client
// otherwise. probeHTTPClient returns nil for the direct case, because the
// connection-probe caller has its own default, so the nil is handled here
// rather than dialled through.
func (h *DashboardHandler) clientFor(raw map[string]any) *http.Client {
	if client, _ := h.probeHTTPClient(parseConnectionProbeData(raw), raw); client != nil {
		return client
	}
	return &http.Client{
		Transport: internalproxy.NewSafeTransport(),
		Timeout:   verifyTimeout,
	}
}

// HandleProviderValidate calls the provider's documented /models endpoint with
// a stored credential and records the result.
//
// It moves a provider to "configured" and never further: a model list says the
// credential authenticates, not that generation works. Only the Test action
// can flip a row to live-verified.
//
// POST /api/provider-status/{id}/validate
func (h *DashboardHandler) HandleProviderValidate(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	entry, ok := status.Lookup(id)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "unknown provider "+id)
		return
	}
	if entry.ModelsEndpoint == nil {
		// The spec's rule in one branch: no documented endpoint means the
		// capability is unavailable, not that we guess where to look.
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"id": id, "status": status.Unavailable, "message": status.ErrEndpointNotDocumented.Error(),
		})
		return
	}

	apiKey, raw, ok := h.credentialFor(id)
	if !ok {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"id": id, "status": status.Unavailable,
			"message": "no stored credential for " + id + " — import one first",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
	defer cancel()

	client := h.clientFor(raw)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *entry.ModelsEndpoint, nil)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	req.Header.Set(constants.HeaderAuthorization, "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"id": id, "status": status.Unavailable, "message": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		// The provider's own error is the useful part: a wrong-key message
		// tells the operator what to fix, a generic "invalid" does not.
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"id": id, "status": status.Unavailable,
			"upstream_status": resp.StatusCode,
			"message":         providerErrorText(body, resp.StatusCode),
		})
		return
	}

	count := countModels(body)
	if err := status.MarkConfigured(id, count); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fresh, _ := status.Lookup(id)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id": id, "status": fresh.Status, "models_available": count,
	})
}

// HandleProviderSmoke sends one minimal real completion and records it as a
// live verification.
//
// This is the only action that can move a row to live-verified, because it is
// the only one that proves a real completion came back. The response body is
// reduced to a character count rather than echoed: the dashboard needs to know
// that text arrived, not to display it, and a transcript is one more place a
// provider's output could be stored.
//
// POST /api/provider-status/{id}/smoke
func (h *DashboardHandler) HandleProviderSmoke(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	entry, ok := status.Lookup(id)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "unknown provider "+id)
		return
	}
	if entry.ChatEndpoint == nil || entry.BaseURL == nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"id": id, "status": status.Unavailable, "message": status.ErrEndpointNotDocumented.Error(),
		})
		return
	}

	apiKey, raw, ok := h.credentialFor(id)
	if !ok {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"id": id, "status": status.Unavailable,
			"message": "no stored credential for " + id + " — import one first",
		})
		return
	}

	model, ok := h.smokeModel(r.Context(), id)
	if !ok {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"id": id, "status": status.Unavailable,
			"message": "no model available for " + id + " — validate first, or set a smoke model",
		})
		return
	}

	client := h.clientFor(raw)
	latency, text, err := h.runSmokeCompletion(r.Context(), client, *entry.ChatEndpoint, apiKey, model)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"id": id, "status": status.Unavailable, "message": err.Error(),
		})
		return
	}
	if text == "" {
		// A 200 with no text is not a completion. Recording it as one would
		// put "live-verified" on a row whose provider never answered.
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"id": id, "status": status.Unavailable,
			"message": "provider returned no text; not recorded as verified",
		})
		return
	}

	if err := status.RecordLiveCompletion(id, model, latency); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Info("provider_status", "live completion recorded",
		"provider", id, "model", model, "latency_ms", latency.Milliseconds())

	fresh, _ := status.Lookup(id)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id": fresh.ID, "status": fresh.Status, "verified_at": fresh.VerifiedAt,
		"model": model, "latency_ms": latency.Milliseconds(),
		"response_chars": len(text),
	})
}

// runSmokeCompletion sends one minimal completion and returns the elapsed
// time and the text that came back.
func (h *DashboardHandler) runSmokeCompletion(
	ctx context.Context, client *http.Client, endpoint, apiKey, model string,
) (time.Duration, string, error) {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	payload := map[string]any{
		"model":      model,
		"messages":   []any{map[string]any{"role": "user", "content": "Reply with the single word: ok"}},
		"max_tokens": verifyMaxTokens,
		"stream":     false,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	req.Header.Set(constants.HeaderAuthorization, "Bearer "+apiKey)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	latency := time.Since(start)

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return latency, "", fmt.Errorf("provider returned HTTP %d: %s",
			resp.StatusCode, providerErrorText(body, resp.StatusCode))
	}
	return latency, completionText(body), nil
}

// completionText pulls the assistant text out of an OpenAI-shaped completion.
func completionText(body []byte) string {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	for _, c := range resp.Choices {
		if c.Message.Content != "" {
			return c.Message.Content
		}
		if c.Text != "" {
			return c.Text
		}
	}
	return ""
}

// countModels reads the length of an OpenAI-shaped /models list. A body that
// is not a list yields 0 rather than a guess.
func countModels(body []byte) int {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0
	}
	return len(resp.Data)
}

// providerErrorText returns the provider's own message when it sent one, so
// the dashboard shows the real cause ("invalid api key") rather than a
// reworded version of it.
func providerErrorText(body []byte, statusCode int) string {
	var resp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
		Detail  any    `json:"detail"`
	}
	if err := json.Unmarshal(body, &resp); err == nil {
		if resp.Error.Message != "" {
			return resp.Error.Message
		}
		if resp.Message != "" {
			return resp.Message
		}
	}
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 400 {
		trimmed = trimmed[:400] + "…"
	}
	if trimmed == "" {
		return http.StatusText(statusCode)
	}
	return trimmed
}

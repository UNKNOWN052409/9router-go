package oauth

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Nous Research OAuth — RFC 8628 device code flow, ported from upstream
// decolua/9router `src/lib/oauth/providers/nous.js` + `open-sse/services/nous.js`.
//
// Nous is the one upstream provider that uses textbook device authorization:
// the CLI mints a device code from the portal, the user opens
// portal.nousresearch.com/manage-subscription, and the CLI polls the token
// endpoint with `grant_type=urn:ietf:params:oauth:grant-type:device_code`.
//
// After the exchange, the access token is immediately spent on minting an
// agent key at {agentKeyUrl} (min TTL 7200s) — that key is what authenticates
// inference traffic at inference-api.nousresearch.com, and what lands in
// `data.apiKey`. If minting fails, upstream falls back to the raw access
// token (mapTokens: `apiKey: extra?.agentKey || tokens.access_token`), so a
// transient mint failure must never fail the login.
// URL endpoints are vars so tests can point the flow at an httptest fake
// without touching prod constants.
var (
	nousDeviceCodeURL = "https://portal.nousresearch.com/api/oauth/device/code"
	nousTokenURL      = "https://portal.nousresearch.com/api/oauth/token"
	nousAgentKeyURL   = "https://portal.nousresearch.com/api/oauth/agent-key"
	nousInferenceBase = "https://inference-api.nousresearch.com/v1"
)

const (
	nousClientID        = "hermes-cli"
	nousScope           = "inference:invoke inference:mint_agent_key"
	nousAgentKeyMinTTL  = 7200
	nousValidationModel = "poolside/laguna-xs-2.1:free"
)

// nousStart opens an RFC 8628 device flow and returns the device envelope.
func nousStart() (map[string]any, error) {
	form := url.Values{}
	form.Set("client_id", nousClientID)
	form.Set("scope", nousScope)
	data, status, err := postForm(nousDeviceCodeURL, form, nil)
	if err != nil {
		return nil, fmt.Errorf("nous: device code request failed: %v", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("nous: device code request failed: HTTP %d", status)
	}
	deviceCode := strVal(data, "device_code")
	if deviceCode == "" {
		return nil, fmt.Errorf("nous: device code response missing device_code")
	}
	verificationURI := strVal(data, "verification_uri")
	if verificationURI == "" {
		return nil, fmt.Errorf("nous: device code response missing verification_uri")
	}
	interval := 5
	if v, ok := data["interval"].(float64); ok && v > 0 {
		interval = int(v)
	}
	expiresIn := 600
	if v, ok := data["expires_in"].(float64); ok && v > 0 {
		expiresIn = int(v)
	}
	return map[string]any{
		"device_code":               deviceCode,
		"user_code":                 strVal(data, "user_code"),
		"verification_uri":          verificationURI,
		"verification_uri_complete": strVal(data, "verification_uri_complete"),
		"expires_in":                expiresIn,
		"interval":                  interval,
	}, nil
}

// nousPoll advances the device flow one step. authorization_pending and
// slow_down are reported as errors containing "pending" because devicePoll
// already collapses that marker into the `pending` status the frontend
// polls against.
func nousPoll(deviceCode string) (deviceTokens, error) {
	var t deviceTokens
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("client_id", nousClientID)
	form.Set("device_code", deviceCode)
	data, status, err := postForm(nousTokenURL, form, nil)
	if err != nil {
		return t, fmt.Errorf("nous: token request failed: %v", err)
	}
	if status != 200 {
		// RFC 8628 says pending and slow_down arrive as errors with status 400;
		// the exchange itself is fine, the user has not finished yet.
		if nousPending(data) {
			return t, fmt.Errorf("nous: authorization_pending")
		}
		if msg := nousErrorDescription(data); msg != "" {
			return t, fmt.Errorf("nous: token request failed: HTTP %d (%s)", status, msg)
		}
		return t, fmt.Errorf("nous: token request failed: HTTP %d", status)
	}
	access := strVal(data, "access_token")
	if access == "" {
		return t, fmt.Errorf("nous: token response missing access_token")
	}
	t.access = access
	t.refresh = strVal(data, "refresh_token")
	if v, ok := data["expires_in"].(float64); ok && v > 0 {
		t.expiresIn = int(v)
	}
	t.email = nousEmailFromToken(access)

	// Mint the agent key the inference API actually wants. Upstream treats a
	// mint failure as a warning and falls back to the access token, so do the
	// same: a failed mint is not a failed login.
	key, keyID, keyExpiresAt, keyExpiresIn := nousMintAgentKey(access)
	apiKey := access
	if key != "" {
		apiKey = key
	}
	t.access = apiKey
	t.extra = map[string]any{
		"authMethod":        "device",
		"inferenceBaseUrl":  nousInferenceBase,
		"scope":             strVal(data, "scope"),
		"agentKey":          key,
		"agentKeyId":        keyID,
		"agentKeyExpiresAt": keyExpiresAt,
		"agentKeyExpiresIn": keyExpiresIn,
	}
	return t, nil
}

func nousPending(data map[string]any) bool {
	switch strVal(data, "error") {
	case "authorization_pending", "slow_down":
		return true
	}
	return false
}

func nousErrorDescription(data map[string]any) string {
	desc := strVal(data, "error_description")
	if desc != "" {
		return desc
	}
	return strVal(data, "error")
}

func nousEmailFromToken(access string) string {
	claims, ok := decodeJWTClaims(access)
	if !ok {
		return ""
	}
	if email := strVal(claims, "email"); email != "" {
		return email
	}
	if sub, ok := claims["sub"].(string); ok && strings.Contains(sub, "@") {
		return sub
	}
	return ""
}

// nousMintAgentKey exchanges the OAuth access token for a long-lived agent key.
// Returns empty values when the mint fails; the caller falls back to the raw
// access token exactly like upstream mapTokens does.
func nousMintAgentKey(access string) (key, keyID, expiresAt string, expiresIn int) {
	data, status, err := postJSON(nousAgentKeyURL,
		map[string]any{"min_ttl_seconds": nousAgentKeyMinTTL},
		map[string]string{"Authorization": "Bearer " + access})
	if err != nil || status != http.StatusOK {
		return "", "", "", 0
	}
	key = strings.TrimSpace(strVal(data, "api_key"))
	keyID = strVal(data, "key_id")
	expiresAt = strVal(data, "expires_at")
	if v, ok := data["expires_in"].(float64); ok && v > 0 {
		expiresIn = int(v)
	}
	return key, keyID, expiresAt, expiresIn
}

// nousValidateKey probes an api key against the validation model with a
// one-token request. Nous exposes /models publicly, so only an authenticated
// inference request proves the credential was accepted.
func nousValidateKey(apiKey string) (ok bool, status int, body string) {
	payload := map[string]any{
		"model":      nousValidationModel,
		"messages":   []any{map[string]any{"role": "user", "content": "ping"}},
		"max_tokens": 1,
		"stream":     false,
	}
	data, status, err := postJSON(nousInferenceBase+"/chat/completions", payload,
		map[string]string{"Authorization": "Bearer " + apiKey})
	if err != nil {
		return false, 0, err.Error()
	}
	if status >= 200 && status < 300 {
		return true, status, ""
	}
	body = strVal(data, "error", "message")
	return false, status, body
}

package oauth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Zai GLM Coding OAuth — CLI polling flow, ported from upstream decolua/9router
// `src/lib/oauth/providers/glm.js` (which itself mirrors the official ZCode CLI,
// `apps/zcode-cli packages/adapters/src/auth/cli-oauth.ts` +
// `coding-plan-api-key.ts`).
//
// It is NOT RFC 8628 device code and carries no PKCE, because the browser never
// redirects to a local listener: the CLI opens an authorize URL, then polls a
// server-side flow for the result.
//
//  1. POST {cliInitURL} with `Authorization: Bearer <pollToken>` and
//     {"provider":"zai"} → {code:0, data:{authorize_url, flow_id,
//     poll_interval_sec, expires_at}}
//  2. The browser opens authorize_url and the user signs in to their Z.ai account.
//  3. GET {cliPollURL}/<flow_id> with the same bearer → {data:{status:pending}}
//     until {data:{status:ready, token, user, zai:{access_token, ...}}}.
//  4. The Z.ai OAuth token is exchanged for a platform business JWT via
//     POST {businessLoginURL} {"token": ...}.
//  5. That JWT mints a long-lived coding-plan key: getCustomerInfo → default
//     org + non-team project → api_keys list/create("zcode-api-key") → copy →
//     "<apiKey>.<secretKey>".
//
// Step 5's composite key is what actually authenticates model traffic, so it is
// what lands in `data.apiKey`. The Z.ai OAuth token has no refresh grant of its
// own and the business JWT expires, but the coding-plan key does not — an
// expired login means re-running the flow, never a dead connection. That is the
// same lifetime the official CLI offers.
const (
	glmCLIInitURL     = "https://zcode.z.ai/api/v1/oauth/cli/init"
	glmCLIPollURL     = "https://zcode.z.ai/api/v1/oauth/cli/poll"
	glmAPIBaseURL     = "https://api.z.ai"
	glmBusinessLogin  = glmAPIBaseURL + "/api/auth/z/login"
	glmPlanAPIKeyName = "zcode-api-key"

	// glmPollTokenSessionKey is where the bearer minted in step 1 is parked
	// between start and poll. The frontend round-trips `session` verbatim
	// (HandleDeviceStart → HandleDevicePoll), which is how kimi carries its
	// device id too.
	glmPollTokenSessionKey = "zcodePollToken"
)

// glmStart opens a ZCode CLI OAuth flow and returns the device-flow envelope the
// dashboard already knows how to render: device_code carries the flow id (not a
// real device code), verification_uri carries the browser authorize URL.
func glmStart() (map[string]any, error) {
	pollToken, err := randomHex(32)
	if err != nil {
		return nil, fmt.Errorf("glm: mint poll token: %w", err)
	}
	headers := map[string]string{"Authorization": "Bearer " + pollToken}
	body, status, err := postJSON(glmCLIInitURL, map[string]string{"provider": "zai"}, headers)
	if err != nil || status != 200 {
		return nil, fmt.Errorf("glm: ZCode init failed: status=%d err=%v", status, err)
	}
	if !glmSuccess(body) {
		return nil, fmt.Errorf("glm: ZCode init error %v: %s", glmCode(body), strVal(body, "msg"))
	}
	data, _ := body["data"].(map[string]any)
	flowID := strVal(data, "flow_id")
	authorizeURL := strVal(data, "authorize_url")
	if flowID == "" || authorizeURL == "" {
		return nil, fmt.Errorf("glm: ZCode init response missing flow_id/authorize_url")
	}
	interval := 3
	if v, ok := data["poll_interval_sec"].(float64); ok && v > 0 {
		interval = int(v)
	}
	return map[string]any{
		"device_code":              flowID,
		"user_code":                "",
		"verification_uri":         authorizeURL,
		"verification_uri_complete": authorizeURL,
		"interval":                 interval,
		"expires_in":               glmExpiresIn(data),
		"session":                  map[string]any{glmPollTokenSessionKey: pollToken},
	}, nil
}

// glmExpiresIn converts upstream's absolute `expires_at` into a relative
// duration, falling back to five minutes when the field is absent or already
// past — the dashboard needs a countdown, not a wall-clock timestamp.
func glmExpiresIn(data map[string]any) int {
	raw, ok := data["expires_at"].(float64)
	if !ok || raw <= 0 {
		return 300
	}
	secs := int(raw) - int(time.Now().Unix())
	if secs <= 0 {
		return 300
	}
	return secs
}

// glmPoll advances the flow one step. A pending state is reported as an error
// containing "pending" because devicePoll already collapses that marker into the
// `pending` status the frontend polls against.
func glmPoll(session map[string]any, flowID string) (deviceTokens, error) {
	var t deviceTokens
	pollToken := strVal(session, glmPollTokenSessionKey)
	if pollToken == "" {
		return t, fmt.Errorf("glm: authorization_pending: missing ZCode poll token, restart the login")
	}
	body, status, err := getJSON(glmCLIPollURL+"/"+url.PathEscape(flowID), map[string]string{"Authorization": "Bearer " + pollToken})
	if err != nil || status != 200 {
		return t, fmt.Errorf("glm: ZCode poll failed: status=%d err=%v", status, err)
	}
	if !glmSuccess(body) {
		return t, fmt.Errorf("glm: ZCode poll error %v: %s", glmCode(body), strVal(body, "msg"))
	}
	data, _ := body["data"].(map[string]any)
	switch strVal(data, "status") {
	case "pending":
		return t, fmt.Errorf("glm: authorization_pending")
	case "failed":
		return t, fmt.Errorf("glm: authorization was cancelled or failed")
	case "ready":
	default:
		return t, fmt.Errorf("glm: authorization_pending: unknown ZCode status %q", strVal(data, "status"))
	}

	// The Z.ai OAuth token nests under the provider id; fall back to top-level
	// fields so payload drift upstream cannot silently produce an empty login.
	providerData, _ := data["zai"].(map[string]any)
	zaiToken := strVal(providerData, "access_token", "accessToken")
	if zaiToken == "" {
		zaiToken = strVal(data, "access_token", "accessToken")
	}
	if zaiToken == "" {
		return t, fmt.Errorf("glm: ZCode poll response missing access token")
	}

	planKey, businessToken, err := glmResolveCodingPlanKey(zaiToken)
	if err != nil {
		return t, err
	}
	user, _ := data["user"].(map[string]any)

	t.access = planKey
	t.name = strVal(user, "name", "email")
	t.email = strVal(user, "email")
	// Upstream persists expiresAt=null here: the coding-plan key is long-lived
	// and the session's own TTL is not a per-token value worth guessing at.
	t.extra = map[string]any{
		"authMethod":         "cli_poll",
		"zcodeJwtToken":      strVal(data, "token"),
		"zaiBusinessToken":   businessToken,
		"zaiRefreshToken":    strVal(providerData, "refresh_token", "refreshToken"),
		"username":           strVal(user, "name"),
		"userId":             strVal(user, "user_id"),
		"planApiKeyName":     glmPlanAPIKeyName,
		"codingPlanKeyParts": 2,
	}
	return t, nil
}

// glmResolveCodingPlanKey walks the business-API chain that turns a Z.ai OAuth
// token into the "<apiKey>.<secretKey>" credential model traffic actually uses.
func glmResolveCodingPlanKey(zaiToken string) (planKey, businessToken string, err error) {
	businessToken, err = glmExchangeBusinessToken(zaiToken)
	if err != nil {
		return "", "", err
	}
	headers := map[string]string{"Authorization": "Bearer " + businessToken}

	info, err := glmBusinessJSON(glmAPIBaseURL+"/api/biz/customer/getCustomerInfo", "GET", nil, headers, "customer info")
	if err != nil {
		return "", "", err
	}
	orgID, projectID := glmPickOrgAndProject(info)
	if orgID == "" || projectID == "" {
		return "", "", fmt.Errorf("glm: no personal Z.ai organization/project exposes a coding plan")
	}
	listURL := fmt.Sprintf("%s/api/biz/v1/organization/%s/projects/%s/api_keys", glmAPIBaseURL, orgID, projectID)

	keys, err := glmBusinessJSON(listURL, "GET", nil, headers, "api key list")
	if err != nil {
		return "", "", err
	}
	entry := glmFindPlanKey(keys)
	if entry == nil {
		entry, err = glmBusinessJSON(listURL, "POST", map[string]string{"name": glmPlanAPIKeyName}, headers, "api key create")
		if err != nil {
			return "", "", err
		}
	}
	apiKey := strings.TrimSpace(strVal(entry, "apiKey"))
	if apiKey == "" {
		return "", "", fmt.Errorf("glm: Z.ai api_keys response is missing apiKey")
	}
	copied, err := glmBusinessJSON(listURL+"/copy/"+url.PathEscape(apiKey), "GET", nil, headers, "api key copy")
	if err != nil {
		return "", "", err
	}
	secret := strings.TrimSpace(strVal(copied, "secretKey"))
	if secret == "" {
		return "", "", fmt.Errorf("glm: Z.ai api key copy response is missing secretKey")
	}
	return apiKey + "." + secret, businessToken, nil
}

// glmExchangeBusinessToken swaps the Z.ai OAuth token for the platform business
// JWT that authorizes the /api/biz/* calls.
func glmExchangeBusinessToken(zaiToken string) (string, error) {
	data, err := glmBusinessJSON(glmBusinessLogin, "POST", map[string]string{"token": zaiToken}, nil, "business login")
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(strVal(data, "access_token", "accessToken"))
	if token == "" {
		return "", fmt.Errorf("glm: Z.ai business login response is missing access_token")
	}
	return token, nil
}

// glmBusinessJSON performs one business-API call and unwraps its {code,msg,data}
// envelope, where success is code 0/200 or an absent code.
func glmBusinessJSON(target, method string, body any, headers map[string]string, label string) (map[string]any, error) {
	reqHeaders := map[string]string{}
	for k, v := range headers {
		reqHeaders[k] = v
	}
	var (
		payload map[string]any
		status  int
		err     error
	)
	if method == "GET" {
		payload, status, err = getJSON(target, reqHeaders)
	} else {
		reqHeaders["Content-Type"] = "application/json"
		payload, status, err = postJSON(target, body, reqHeaders)
	}
	if err != nil || status != 200 {
		return nil, fmt.Errorf("glm: %s request failed: status=%d err=%v", label, status, err)
	}
	if !glmSuccess(payload) || payload["success"] == false {
		return nil, fmt.Errorf("glm: %s error %v: %s", label, glmCode(payload), strVal(payload, "msg"))
	}
	if data, ok := payload["data"].(map[string]any); ok {
		return data, nil
	}
	// Some endpoints answer with a bare array or object rather than {data:...}.
	return payload, nil
}

// glmFindPlanKey locates the existing coding-plan key so a re-login reuses it
// instead of minting a duplicate on every fresh authorize.
func glmFindPlanKey(keys map[string]any) map[string]any {
	list, ok := keys["apiKeys"].([]any)
	if !ok {
		list, ok = keys["list"].([]any)
	}
	if !ok {
		return nil
	}
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if ok && strVal(entry, "name") == glmPlanAPIKeyName {
			return entry
		}
	}
	return nil
}

// glmPickOrgAndProject selects the account's personal org (默认机构/default) and
// its non-team project — projectType "2" is a team space, whose keys are not
// what the CLI is entitled to.
func glmPickOrgAndProject(info map[string]any) (orgID, projectID string) {
	orgs, _ := info["organizations"].([]any)
	type candidate struct {
		org      map[string]any
		projects []any
	}
	var personal []candidate
	for _, raw := range orgs {
		org, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		all, _ := org["projects"].([]any)
		var solo []any
		for _, p := range all {
			project, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if strings.TrimSpace(strVal(project, "projectType")) == "2" {
				continue
			}
			solo = append(solo, project)
		}
		if strVal(org, "organizationId") != "" && len(solo) > 0 {
			personal = append(personal, candidate{org: org, projects: solo})
		}
	}
	if len(personal) == 0 {
		return "", ""
	}
	chosen := personal[0]
	for _, c := range personal {
		if glmIsDefaultName(strVal(c.org, "organizationName")) {
			chosen = c
			break
		}
	}
	for _, p := range chosen.projects {
		project, _ := p.(map[string]any)
		if project == nil {
			continue
		}
		if glmIsDefaultName(strVal(project, "projectName")) {
			return strVal(chosen.org, "organizationId"), strVal(project, "projectId")
		}
	}
	first, _ := chosen.projects[0].(map[string]any)
	if first == nil {
		return "", ""
	}
	return strVal(chosen.org, "organizationId"), strVal(first, "projectId")
}

func glmIsDefaultName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(n, "默认机构") || strings.Contains(n, "默认项目") || n == "default"
}

// glmSuccess reports whether a {code,msg,data} envelope is a success. Z.ai
// treats 0 and 200 as success and some responses omit the code entirely.
//
// The code is compared as a float64 because that is what every JSON number
// decodes to here: comparing the raw `any` against an int constant never
// matches, which would reject every real success envelope.
func glmSuccess(payload map[string]any) bool {
	raw, present := payload["code"]
	if !present {
		return true
	}
	code, ok := raw.(float64)
	if !ok {
		// A code that is not a number is not a status we recognise as success.
		return false
	}
	return code == 0 || code == 200 || code == -1
}

func glmCode(payload map[string]any) any {
	if v, ok := payload["code"]; ok {
		return v
	}
	return "none"
}

// randomHex returns n random bytes hex-encoded — the poll token Z.ai expects as
// a 32-byte hex bearer.
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
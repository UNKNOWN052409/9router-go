package executor

import (
	json "encoding/json/v2"
	"strings"
)

// GeminiWebCookieHeader renders a pasted Gemini Web credential as a Cookie
// header. Exported so the dashboard probe reads the credential exactly the way
// the executor does — a probe that accepted a spelling the executor rejected
// would pass while every chat request failed.
func GeminiWebCookieHeader(apiKey string) string {
	return geminiWebCookieHeader(&Request{APIKey: apiKey})
}

// GeminiWebUserAgent is the client string the executor sends. Exported so the
// probe does not carry a second copy that drifts.
func GeminiWebUserAgent() string { return geminiWebUserAgent }

// geminiWebCookieHeader renders a configured credential as a Cookie header.
//
// Three spellings reach here, and all three are in the dashboard's own hint: a
// full cookie header, a bare `__Secure-1PSID` value, and a Cookie-Editor JSON
// export. A credential that is none of them is passed through unchanged, which
// is the only reading left.
func geminiWebCookieHeader(req *Request) string {
	raw := strings.TrimSpace(req.APIKey)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "[") || strings.HasPrefix(raw, "{") {
		if expanded, ok := geminiWebExpandCookieJSON(raw); ok {
			return expanded
		}
	}
	if strings.Contains(raw, "=") {
		return raw
	}
	return "__Secure-1PSID=" + raw
}

// geminiWebExpandCookieJSON reads a Cookie-Editor style export into a header.
func geminiWebExpandCookieJSON(raw string) (string, bool) {
	var entries []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil || len(entries) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name == "" {
			continue
		}
		parts = append(parts, entry.Name+"="+entry.Value)
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "; "), true
}

package oauth

import (
	"os"
	"strings"
	"testing"
)

// Live check against Z.ai. It only opens a flow and reads the first poll state —
// it never signs in, so no credential is involved and nothing is minted. Skipped
// unless NINE_ROUTER_LIVE_OAUTH=1, keeping the default unit run offline
// (AGENTS.md §5.C).
//
//	NINE_ROUTER_LIVE_OAUTH=1 go test ./internal/handlers/oauth/ -run TestGLMLive -v
func TestGLMLiveStartAndPendingPoll(t *testing.T) {
	if os.Getenv("NINE_ROUTER_LIVE_OAUTH") != "1" {
		t.Skip("set NINE_ROUTER_LIVE_OAUTH=1 to hit zcode.z.ai")
	}

	out, err := glmStart()
	if err != nil {
		t.Fatalf("glmStart against zcode.z.ai: %v", err)
	}
	flowID, _ := out["device_code"].(string)
	authorizeURL, _ := out["verification_uri"].(string)
	if flowID == "" {
		t.Fatal("glmStart returned no flow id")
	}
	if !strings.Contains(authorizeURL, "chat.z.ai") {
		t.Fatalf("authorize_url %q does not point at the Z.ai console", authorizeURL)
	}
	interval, _ := out["interval"].(int)
	if interval <= 0 {
		t.Fatalf("interval = %d, want a positive poll cadence", interval)
	}
	t.Logf("flow opened: interval=%ds expires_in=%v", interval, out["expires_in"])

	// Nobody has authorized this flow, so the poll must report pending. That is
	// the state devicePoll collapses into the frontend's "still waiting".
	session, _ := out["session"].(map[string]any)
	_, err = glmPoll(session, flowID)
	if err == nil {
		t.Fatal("an unauthorized flow reported a token")
	}
	if !strings.Contains(err.Error(), "authorization_pending") {
		t.Fatalf("poll of an unauthorized flow returned %q, want pending", err)
	}
}
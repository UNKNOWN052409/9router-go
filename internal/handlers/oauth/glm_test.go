package oauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The GLM flow's value is in its envelope shapes, not its happy path: every
// helper that parses a Z.ai response has to survive a code it does not expect
// without minting a broken connection. These tests pin those shapes.

func TestGLMSuccess(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		want    bool
	}{
		{"code zero is success", map[string]any{"code": float64(0)}, true},
		{"code 200 is success", map[string]any{"code": float64(200)}, true},
		{"absent code is success", map[string]any{"data": map[string]any{}}, true},
		{"business error is failure", map[string]any{"code": float64(3004), "msg": "invalid_flow"}, false},
		{"other numeric code fails", map[string]any{"code": float64(500)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := glmSuccess(tt.payload); got != tt.want {
				t.Fatalf("glmSuccess(%v) = %v, want %v", tt.payload, got, tt.want)
			}
		})
	}
}

func TestGLMExpiresIn(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		want int
	}{
		{"missing field falls back to five minutes", map[string]any{}, 300},
		{"zero falls back", map[string]any{"expires_at": float64(0)}, 300},
		{"past deadline falls back rather than going negative", map[string]any{"expires_at": float64(1)}, 300},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := glmExpiresIn(tt.data); got != tt.want {
				t.Fatalf("glmExpiresIn(%v) = %d, want %d", tt.data, got, tt.want)
			}
		})
	}
}

func TestGLMIsDefaultName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"chinese default org", "默认机构", true},
		{"chinese default project", "默认项目", true},
		{"english default", "default", true},
		{"default is case-insensitive", "Default", true},
		{"an ordinary org is not default", "Acme Research", false},
		{"empty is not default", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := glmIsDefaultName(tt.in); got != tt.want {
				t.Fatalf("glmIsDefaultName(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestGLMPickOrgAndProject(t *testing.T) {
	tests := []struct {
		name          string
		info          map[string]any
		wantOrg       string
		wantProject   string
		wantSelection bool
	}{
		{
			name: "prefers the default org and project",
			info: map[string]any{"organizations": []any{
				map[string]any{
					"organizationId":   "org-b",
					"organizationName": "默认机构",
					"projects": []any{
						map[string]any{"projectId": "proj-b2", "projectName": "默认项目"},
						map[string]any{"projectId": "proj-b1", "projectName": "Side Project"},
					},
				},
				map[string]any{
					"organizationId":   "org-a",
					"organizationName": "Acme",
					"projects":         []any{map[string]any{"projectId": "proj-a1", "projectName": "Acme Project"}},
				},
			}},
			wantOrg: "org-b", wantProject: "proj-b2", wantSelection: true,
		},
		{
			name: "skips a team project (projectType 2)",
			info: map[string]any{"organizations": []any{
				map[string]any{
					"organizationId":   "org-a",
					"organizationName": "Acme",
					"projects": []any{
						map[string]any{"projectId": "team-1", "projectName": "默认项目", "projectType": "2"},
					},
				},
			}},
			wantSelection: false,
		},
		{
			name: "falls back to the first org with a personal project",
			info: map[string]any{"organizations": []any{
				map[string]any{
					"organizationId":   "org-a",
					"organizationName": "Acme",
					"projects":         []any{map[string]any{"projectId": "proj-a1"}},
				},
			}},
			wantOrg: "org-a", wantProject: "proj-a1", wantSelection: true,
		},
		{
			name:          "an org with no projects is unusable",
			info:          map[string]any{"organizations": []any{map[string]any{"organizationId": "org-a", "projects": []any{}}}},
			wantSelection: false,
		},
		{
			name:          "no organizations at all",
			info:          map[string]any{"organizations": []any{}},
			wantSelection: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			org, project := glmPickOrgAndProject(tt.info)
			if (org != "") != tt.wantSelection {
				t.Fatalf("glmPickOrgAndProject selection = %v (org=%q), want %v", org != "", org, tt.wantSelection)
			}
			if org != tt.wantOrg || project != tt.wantProject {
				t.Fatalf("glmPickOrgAndProject = (%q, %q), want (%q, %q)", org, project, tt.wantOrg, tt.wantProject)
			}
		})
	}
}

// A missing poll token must read as "still waiting", never as a hard error: the
// frontend polls on a timer, and an error would strand the modal on an error.
func TestGLMPollWithoutTokenStaysPending(t *testing.T) {
	_, err := glmPoll(map[string]any{}, "flow-123")
	if err == nil {
		t.Fatal("glmPoll with no session token returned no error")
	}
	if got := err.Error(); !strings.Contains(got, "authorization_pending") {
		t.Fatalf("error %q does not report pending; the frontend would show it as a failure", got)
	}
}

func TestGLMFindPlanKey(t *testing.T) {
	keyed := map[string]any{"apiKeys": []any{
		map[string]any{"name": "some-other-key", "apiKey": "nope"},
		map[string]any{"name": "zcode-api-key", "apiKey": "yes"},
	}}
	if entry := glmFindPlanKey(keyed); strVal(entry, "apiKey") != "yes" {
		t.Fatalf("glmFindPlanKey did not select zcode-api-key, got %v", entry)
	}
	if entry := glmFindPlanKey(map[string]any{"apiKeys": []any{map[string]any{"name": "other"}}}); entry != nil {
		t.Fatalf("glmFindPlanKey invented a match: %v", entry)
	}
}

// The whole point of the chain is that the composite key is what model traffic
// authenticates with, so an endpoint that returns one half must not produce a
// credential that silently fails at request time.
func TestGLMBusinessJSONPropagatesBusinessErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1001,"msg":"Authentication parameter not received in Header"}`))
	}))
	defer server.Close()

	if _, err := glmBusinessJSON(server.URL, "GET", nil, nil, "customer info"); err == nil {
		t.Fatal("a 1001 business envelope was accepted as success")
	}
}

func TestGLMBusinessJSONUnwrapsData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"biz-jwt"}}`))
	}))
	defer server.Close()

	data, err := glmBusinessJSON(server.URL, "POST", map[string]string{"token": "t"}, nil, "business login")
	if err != nil {
		t.Fatalf("glmBusinessJSON on a success envelope: %v", err)
	}
	if got := strVal(data, "access_token"); got != "biz-jwt" {
		t.Fatalf("access_token = %q, want biz-jwt", got)
	}
}
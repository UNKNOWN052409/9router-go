package chat

import (
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestApplyTokenSavers_PersonaInjectsOpenAIFormat(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetPersonas(map[string]db.PersonaSettings{
		"deepseek": {Name: "ds", SystemPrompt: "You are a terse coder.", Enabled: true},
	})

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got, _, _, _ := h.applyTokenSavers(body, false, "deepseek", "deepseek-chat")
	if !strings.Contains(string(got), "You are a terse coder.") {
		t.Fatalf("expected persona system prompt injected, got %s", got)
	}
}

func TestApplyTokenSavers_PersonaInjectsClaudeFormat(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetPersonas(map[string]db.PersonaSettings{
		"anthropic": {Name: "claude", SystemPrompt: "You are Claude with warm empathy.", Enabled: true},
	})

	// Claude Messages: NO role:"system" message is allowed; the persona must
	// land in the top-level "system" field instead.
	body := []byte(`{"model":"claude-sonnet-4","max_tokens":128,"messages":[{"role":"user","content":"hi"}]}`)
	got, _, _, _ := h.applyTokenSavers(body, true, "anthropic", "claude-sonnet-4")
	out := string(got)
	if !strings.Contains(out, "You are Claude with warm empathy.") {
		t.Fatalf("expected persona in Claude system field, got %s", out)
	}
	if strings.Contains(out, `"role":"system"`) {
		t.Fatalf("persona must not create a role system message in Claude format, got %s", out)
	}
}

func TestApplyTokenSavers_PersonaModelScopeWins(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetPersonas(map[string]db.PersonaSettings{
		"deepseek":                   {Name: "provider", SystemPrompt: "PROVIDER-PROMPT", Enabled: true},
		"deepseek/deepseek-reasoner": {Name: "model", SystemPrompt: "MODEL-PROMPT", Enabled: true},
	})

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got, _, _, _ := h.applyTokenSavers(body, false, "deepseek", "deepseek-reasoner")
	out := string(got)
	if !strings.Contains(out, "MODEL-PROMPT") {
		t.Fatalf("expected model-scoped persona, got %s", out)
	}
	if strings.Contains(out, "PROVIDER-PROMPT") {
		t.Fatalf("model-scoped persona must suppress provider-scoped one, got %s", out)
	}
}

func TestApplyTokenSavers_PersonaDoesNotDuplicateExisting(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetPersonas(map[string]db.PersonaSettings{
		"deepseek": {Name: "ds", SystemPrompt: "You are a terse coder.", Enabled: true},
	})

	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"model":"deepseek-chat"}`)
	// First pass injects the persona.
	got, _, _, _ := h.applyTokenSavers(body, false, "deepseek", "deepseek-chat")
	// Second run on the already-injected body must be a no-op (dedupe).
	again, _, _, _ := h.applyTokenSavers(got, false, "deepseek", "deepseek-chat")
	if strings.Count(string(again), "You are a terse coder.") != 1 {
		t.Fatalf("expected exactly one persona injection after dedupe, got %q", again)
	}
}

func TestApplyTokenSavers_PersonaUnmatchedIsNoop(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetPersonas(map[string]db.PersonaSettings{
		"deepseek": {Name: "ds", SystemPrompt: "You are a terse coder.", Enabled: true},
	})

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got, _, _, _ := h.applyTokenSavers(body, false, "openai", "gpt-4o")
	if string(got) != string(body) {
		t.Fatalf("expected unchanged body for unmatched provider, got %s", got)
	}
}

func TestApplyTokenSavers_PersonaDisabledIsNoop(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()
	h.TokenSaver.SetPersonas(map[string]db.PersonaSettings{
		"deepseek": {Name: "ds", SystemPrompt: "You are a terse coder.", Enabled: false},
	})

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got, _, _, _ := h.applyTokenSavers(body, false, "deepseek", "deepseek-chat")
	if string(got) != string(body) {
		t.Fatalf("expected unchanged body when persona disabled, got %s", got)
	}
}

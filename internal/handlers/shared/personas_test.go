package shared

import (
	"testing"

	"9router/proxy/internal/db"
)

func personaProvider(name, prompt string) db.PersonaSettings {
	return db.PersonaSettings{Name: name, SystemPrompt: prompt, Enabled: true}
}

func TestPersonaFor_ReturnsProviderScoped(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	c.SetPersonas(map[string]db.PersonaSettings{
		"deepseek": personaProvider("ds", "You are DeepSeek."),
	})

	got, ok := c.PersonaFor("deepseek", "deepseek-chat")
	if !ok {
		t.Fatal("expected a persona for provider deepseek")
	}
	if got.SystemPrompt != "You are DeepSeek." {
		t.Errorf("got prompt %q, want provider-scoped prompt", got.SystemPrompt)
	}
}

func TestPersonaFor_ModelScopedOutranksProvider(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	c.SetPersonas(map[string]db.PersonaSettings{
		"deepseek":                   personaProvider("provider", "Provider prompt."),
		"deepseek/deepseek-reasoner": personaProvider("model", "Model prompt."),
	})

	got, ok := c.PersonaFor("deepseek", "deepseek-reasoner")
	if !ok {
		t.Fatal("expected a persona for deepseek/deepseek-reasoner")
	}
	if got.SystemPrompt != "Model prompt." {
		t.Errorf("got %q, want model-scoped prompt to outrank provider", got.SystemPrompt)
	}
}

func TestPersonaFor_ModelScopeFallsBackToProvider(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	c.SetPersonas(map[string]db.PersonaSettings{
		"deepseek": personaProvider("ds", "Provider prompt."),
	})

	got, ok := c.PersonaFor("deepseek", "some-other-model")
	if !ok {
		t.Fatal("expected provider-scoped fallback")
	}
	if got.SystemPrompt != "Provider prompt." {
		t.Errorf("got %q, want provider fallback", got.SystemPrompt)
	}
}

func TestPersonaFor_DisabledOrEmptyIsAbsent(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		p     db.PersonaSettings
		model string
	}{
		{name: "disabled", key: "deepseek", p: db.PersonaSettings{SystemPrompt: "x", Enabled: false}, model: "deepseek-chat"},
		{name: "empty prompt", key: "deepseek", p: db.PersonaSettings{SystemPrompt: "   ", Enabled: true}, model: "deepseek-chat"},
		{name: "whitespace prompt", key: "deepseek", p: db.PersonaSettings{SystemPrompt: "\n\t", Enabled: true}, model: "deepseek-chat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewTokenSaverConfig(false, false, false)
			c.SetPersonas(map[string]db.PersonaSettings{tt.key: tt.p})
			if _, ok := c.PersonaFor("deepseek", tt.model); ok {
				t.Errorf("expected no persona for %s", tt.name)
			}
		})
	}
}

func TestPersonaFor_NoMatchAndEmptyMap(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	if _, ok := c.PersonaFor("deepseek", "deepseek-chat"); ok {
		t.Error("expected no persona with empty map")
	}
	c.SetPersonas(map[string]db.PersonaSettings{"other": personaProvider("o", "x")})
	if _, ok := c.PersonaFor("deepseek", "deepseek-chat"); ok {
		t.Error("expected no persona for unmatched provider")
	}
}

func TestSetPersonas_ReplacesWholesaleAndNilClears(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	c.SetPersonas(map[string]db.PersonaSettings{"a": personaProvider("a", "1")})
	c.SetPersonas(map[string]db.PersonaSettings{"b": personaProvider("b", "2")})
	if _, ok := c.PersonaFor("a", "x"); ok {
		t.Error("old persona must be gone after wholesale replacement")
	}
	got, ok := c.PersonaFor("b", "x")
	if !ok || got.SystemPrompt != "2" {
		t.Errorf("expected new persona, got ok=%v prompt=%q", ok, got.SystemPrompt)
	}
	c.SetPersonas(nil)
	if n := len(c.Personas()); n != 0 {
		t.Errorf("expected cleared map, got %d entries", n)
	}
}

func TestPersonas_ReturnsDefensiveCopy(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	c.SetPersonas(map[string]db.PersonaSettings{"a": personaProvider("a", "1")})
	got := c.Personas()
	got["a"] = db.PersonaSettings{SystemPrompt: "mutated"}
	if _, ok := c.PersonaFor("a", "x"); !ok {
		t.Fatal("persona must survive a mutation of the returned copy")
	}
	inner, _ := c.PersonaFor("a", "x")
	if inner.SystemPrompt != "1" {
		t.Errorf("expected original prompt 1, got %q", inner.SystemPrompt)
	}
}

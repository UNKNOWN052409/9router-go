package providers

import (
	"strings"
	"testing"
)

// A provider whose catalog entry advertises a media capability must name the
// endpoint that serves it. The catalog is the operator's promise; the media
// handler derives its target from BaseURL whenever no override exists, and
// that derived path is OpenAI-shaped. For a provider that does not speak the
// OpenAI media shape the derived path 404s and the advertised capability is
// silently dead — which is exactly what happened to minimax until these
// overrides were added.
func TestMediaCapableProvidersHaveExplicitEndpoints(t *testing.T) {
	for _, id := range []string{"minimax", "minimax-cn"} {
		cfg, ok := KnownProviders[id]
		if !ok {
			t.Fatalf("%s is not registered", id)
		}
		if cfg.ImageURL == "" {
			t.Errorf("%s is catalogued as an image source but declares no ImageURL, "+
				"so the media handler derives %q", id, "https://api.minimax.io/v1/images/generations")
		}
		if cfg.TTSURL == "" {
			t.Errorf("%s is catalogued as a TTS source but declares no TTSURL", id)
		}
	}
}

// A media override that ends in /generations gets one appended by the video
// branch of the media handler. Image and TTS are used verbatim, so these must
// be the real paths and not a base the handler would rewrite.
func TestMiniMaxMediaOverridesAreRealPaths(t *testing.T) {
	tests := []struct {
		provider string
		host     string
	}{
		{provider: "minimax", host: "api.minimax.io"},
		{provider: "minimax-cn", host: "api.minimaxi.com"},
	}
	for _, tt := range tests {
		cfg := KnownProviders[tt.provider]
		if !strings.Contains(cfg.ImageURL, tt.host) {
			t.Errorf("%s ImageURL points off %s: %s", tt.provider, tt.host, cfg.ImageURL)
		}
		if !strings.HasSuffix(cfg.ImageURL, "/image_generation") {
			t.Errorf("%s ImageURL is not the real path: %s", tt.provider, cfg.ImageURL)
		}
		if !strings.HasSuffix(cfg.TTSURL, "/t2a_v2") {
			t.Errorf("%s TTSURL is not the real path: %s", tt.provider, cfg.TTSURL)
		}
	}
}

// Qwen's model list is discovered from the documented /models endpoint, so the
// registry declares no hardcoded catalog and the provider is marked for live
// discovery instead.
func TestQwenRegistersWithLiveModelDiscovery(t *testing.T) {
	cfg, ok := KnownProviders["qwen"]
	if !ok {
		t.Fatal("qwen is not registered")
	}
	if !strings.Contains(cfg.BaseURL, "dashscope") {
		t.Errorf("qwen base URL is not a DashScope host: %s", cfg.BaseURL)
	}
	if cfg.AuthScheme != "bearer" {
		t.Errorf("qwen auth scheme = %q, want bearer", cfg.AuthScheme)
	}
	if cfg.ImageURL == "" || !strings.Contains(cfg.ImageURL, "text-to-image") {
		t.Errorf("qwen ImageURL is not the DashScope image path: %q", cfg.ImageURL)
	}
	// Video is an async task on DashScope and has no single POST endpoint, so
	// declaring VideoURL would advertise something the gateway cannot serve.
	if cfg.VideoURL != "" {
		t.Errorf("qwen declares a VideoURL it cannot fulfil: %s", cfg.VideoURL)
	}
	// No hardcoded models: an entry here would fight the live /models fetch.
	if _, ok := ProviderModels["qwen"]; ok {
		t.Error("qwen declares a static model catalog despite live discovery")
	}
}

func TestQwenAliases(t *testing.T) {
	if got := ProviderAliasMap["qw"]; got != "qwen" {
		t.Errorf(`ProviderAliasMap["qw"] = %q, want "qwen"`, got)
	}
	if got := RegistryAliases["qwen"]; got != "qw" {
		t.Errorf(`RegistryAliases["qwen"] = %q, want "qw"`, got)
	}
}

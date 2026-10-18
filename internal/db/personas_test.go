package db

import (
	"testing"
)

func TestGetSettings_PersonasParsed(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(database)

	settingsJSON := `{
		"personas": {
			"deepseek": {
				"name": "DeepSeek Coder",
				"description": "Code-first persona",
				"systemPrompt": "You are a concise Go coder.",
				"enabled": true
			},
			"anthropic/claude-sonnet-4": {
				"systemPrompt": "You are Sonnet."
			},
			"openai": "plain string persona"
		}
	}`

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("failed to insert settings: %v", err)
	}

	settings, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}

	if len(settings.Personas) != 3 {
		t.Fatalf("expected 3 personas, got %d", len(settings.Personas))
	}

	ds, ok := settings.Personas["deepseek"]
	if !ok {
		t.Fatal("expected deepseek persona")
	}
	if ds.Name != "DeepSeek Coder" || ds.SystemPrompt != "You are a concise Go coder." || !ds.Enabled {
		t.Errorf("deepseek persona mismatch: %+v", ds)
	}

	sonnet, ok := settings.Personas["anthropic/claude-sonnet-4"]
	if !ok {
		t.Fatal("expected model-scoped persona")
	}
	if sonnet.SystemPrompt != "You are Sonnet." {
		t.Errorf("model-scoped prompt mismatch: %q", sonnet.SystemPrompt)
	}

	openai, ok := settings.Personas["openai"]
	if !ok {
		t.Fatal("expected plain-string persona")
	}
	if openai.SystemPrompt != "plain string persona" || !openai.Enabled {
		t.Errorf("plain-string persona mismatch: %+v", openai)
	}
}

func TestGetSettings_PersonasSkipsMalformed(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(database)

	settingsJSON := `{
		"personas": {
			"good": { "systemPrompt": "ok", "enabled": true },
			"bad-number": 42,
			"bad-array": ["x"],
			"bad-null": null
		}
	}`

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("failed to insert settings: %v", err)
	}

	settings, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}

	if len(settings.Personas) != 1 {
		t.Fatalf("expected only 1 well-formed persona, got %d", len(settings.Personas))
	}
	if _, ok := settings.Personas["good"]; !ok {
		t.Error("expected the well-formed persona to survive")
	}
}

func TestGetSettings_PersonasAbsentWhenKeyMissing(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(database)

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, `{"rtkEnabled": true}`); err != nil {
		t.Fatalf("failed to insert settings: %v", err)
	}

	settings, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}

	if settings.Personas != nil {
		t.Errorf("expected nil Personas when key absent, got %d entries", len(settings.Personas))
	}
}

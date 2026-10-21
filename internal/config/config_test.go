package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"llm-gateway/internal/config"
)

const validYAML = `
upstream:
  - name: openai
    base_url: "http://localhost:6655/v1"
    api_key: "test-key"
    model: "gpt-4.1-mini"

teams:
  - name: teamA
    key_hash: "abc123"
    email: "a@test.com"
    rpm_limit: 100

redis:
  host: "localhost"
  port: 6379
  password: ""

embedding:
  base_url: "http://localhost:6655/v1/embeddings"
  api_key: "test-key"
  model: "text-embedding-3-small"
  similarity_threshold: 0.85
  ttl_seconds: 3600
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(f, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
		check   func(t *testing.T, cfg config.Config)
	}{
		{
			name:    "valid config",
			content: validYAML,
			wantErr: false,
			check: func(t *testing.T, cfg config.Config) {
				if len(cfg.Upstreams) != 1 {
					t.Errorf("upstreams=%d, want 1", len(cfg.Upstreams))
				}
				if cfg.Upstreams[0].Name != "openai" {
					t.Errorf("upstream name=%q, want openai", cfg.Upstreams[0].Name)
				}
				if len(cfg.Teams) != 1 {
					t.Errorf("teams=%d, want 1", len(cfg.Teams))
				}
				if cfg.Teams[0].RpmLimit != 100 {
					t.Errorf("rpm_limit=%d, want 100", cfg.Teams[0].RpmLimit)
				}
				if cfg.Embedding.SimilarityThreshold != 0.85 {
					t.Errorf("similarity_threshold=%v, want 0.85", cfg.Embedding.SimilarityThreshold)
				}
			},
		},
		{
			name:    "invalid yaml",
			content: "upstream: [unclosed",
			wantErr: true,
		},
		{
			name:    "empty file",
			content: "",
			wantErr: false, // valid YAML, just zero values
			check: func(t *testing.T, cfg config.Config) {
				if len(cfg.Upstreams) != 0 {
					t.Errorf("expected no upstreams from empty file")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTemp(t, tt.content)
			cfg, err := config.LoadConfig(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr && tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := config.LoadConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadConfig_RedisEnvOverride(t *testing.T) {
	path := writeTemp(t, validYAML)

	t.Setenv("REDIS_HOST", "redis-override")
	t.Setenv("REDIS_PORT", "6380")

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Redis.Host != "redis-override" {
		t.Errorf("Redis.Host=%q, want redis-override", cfg.Redis.Host)
	}
	if cfg.Redis.Port != 6380 {
		t.Errorf("Redis.Port=%d, want 6380", cfg.Redis.Port)
	}
}

func TestLoadConfig_RedisPasswordEnvOverride(t *testing.T) {
	path := writeTemp(t, validYAML)
	t.Setenv("REDIS_PASSWORD", "secret123")

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Redis.Password != "secret123" {
		t.Errorf("Redis.Password=%q, want secret123", cfg.Redis.Password)
	}
}

func TestLoadConfig_InvalidRedisPortEnvIgnored(t *testing.T) {
	path := writeTemp(t, validYAML)
	t.Setenv("REDIS_PORT", "not-a-number")

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	// invalid port value should be ignored — original config.yaml value kept
	if cfg.Redis.Port != 6379 {
		t.Errorf("Redis.Port=%d, want original 6379 when env var is invalid", cfg.Redis.Port)
	}
}

package auth_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"testing"

	"llm-gateway/internal/auth"
	"llm-gateway/internal/config"
)

func makeTeamMap(token string, team config.Team) map[string]config.Team {
	h := sha256.Sum256([]byte(token))
	return map[string]config.Team{hex.EncodeToString(h[:]): team}
}

func TestAuth(t *testing.T) {
	validTeam := config.Team{Name: "teamA", Email: "a@test.com", RpmLimit: 100}
	teamMap := makeTeamMap("secret-token", validTeam)

	tests := []struct {
		name        string
		header      string
		wantTeam    string
		wantErr     bool
	}{
		{"valid token",             "Bearer secret-token", "teamA", false},
		{"unknown token",           "Bearer wrong-token",  "",      true},
		{"missing Bearer prefix",   "secret-token",        "",      true},
		{"lowercase bearer",        "bearer secret-token", "",      true},
		{"empty header",            "",                    "",      true},
		{"Bearer with empty token", "Bearer ",             "",      true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}

			team, err := auth.Auth(req, teamMap)

			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr && team.Name != tt.wantTeam {
				t.Errorf("team.Name=%q, want %q", team.Name, tt.wantTeam)
			}
		})
	}
}

func TestAuth_PreservesTeamFields(t *testing.T) {
	want := config.Team{Name: "teamB", Email: "b@test.com", RpmLimit: 500}
	teamMap := makeTeamMap("token-b", want)

	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer token-b")

	got, err := auth.Auth(req, teamMap)
	if err != nil {
		t.Fatal(err)
	}
	if got.RpmLimit != want.RpmLimit {
		t.Errorf("RpmLimit=%d, want %d", got.RpmLimit, want.RpmLimit)
	}
	if got.Email != want.Email {
		t.Errorf("Email=%q, want %q", got.Email, want.Email)
	}
}

func BenchmarkAuth(b *testing.B) {
	teamMap := makeTeamMap("bench-token", config.Team{Name: "bench"})
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer bench-token")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = auth.Auth(req, teamMap)
	}
}

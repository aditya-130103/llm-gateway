package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"llm-gateway/internal/config"
)

func Auth( r *http.Request, teamMap map[string]config.Team) (config.Team, error) {
	authHeader := r.Header.Get("Authorization")

	if !strings.HasPrefix(authHeader, "Bearer ") {
		return config.Team{}, errors.New("invalid authorization header")
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	tokenHash := sha256.Sum256([]byte(token))
	team, ok := teamMap[hex.EncodeToString(tokenHash[:])]
	if !ok {
		return config.Team{}, errors.New("invalid token")
	}
	return team, nil
}
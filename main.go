package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	redis "github.com/redis/go-redis/v9"

	"llm-gateway/internal/auth"
	"llm-gateway/internal/cache"
	"llm-gateway/internal/cb"
	"llm-gateway/internal/config"
	"llm-gateway/internal/metrics"
	"llm-gateway/internal/proxy"
	"llm-gateway/internal/ratelimiter"
	"llm-gateway/internal/router"
)

func newRequestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func writeError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	_, _ = fmt.Fprintf(w, "LLM Gateway is running!")
}

func makeChatHandler(teamMap map[string]config.Team, cfg config.Config, breakers map[string]*cb.CB, client *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := newRequestID()

		team, err := auth.Auth(r, teamMap)
		if err != nil {
			writeError(w, err.Error(), http.StatusUnauthorized)
			slog.Error("request failed", "team", "unknown", "requestID", reqID, "statusCode", http.StatusUnauthorized, "error", err)
			metrics.RequestsTotal.WithLabelValues("unknown", "", "401").Inc()
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, "failed to read request body", http.StatusBadRequest)
			slog.Error("request failed", "team", team.Name, "requestID", reqID, "statusCode", http.StatusBadRequest, "error", err)
			metrics.RequestsTotal.WithLabelValues(team.Name, "", "400").Inc()
			return
		}

		var request proxy.Request
		if err = json.Unmarshal(body, &request); err != nil {
			writeError(w, "invalid request body", http.StatusBadRequest)
			slog.Error("request failed", "team", team.Name, "requestID", reqID, "statusCode", http.StatusBadRequest, "error", err)
			metrics.RequestsTotal.WithLabelValues(team.Name, "", "400").Inc()
			return
		}

		if !ratelimiter.AllowRequest(r.Context(), client, team.Name, team.RpmLimit) {
			writeError(w, "rate limit exceeded", http.StatusTooManyRequests)
			slog.Error("request failed", "team", team.Name, "requestID", reqID, "statusCode", http.StatusTooManyRequests)
			metrics.RequestsTotal.WithLabelValues(team.Name, "", "429").Inc()
			return
		}

		var query string
		for i := len(request.Messages) - 1; i >= 0; i-- {
			if request.Messages[i].Role == "user" {
				query = request.Messages[i].Content
				break
			}
		}

		if query != "" {
			if cached, ok := cache.Lookup(r.Context(), client, query, cfg); ok {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(cached)
				slog.Info("request logged", "team", team.Name, "requestID", reqID, "provider", "cache", "statusCode", 200)
				metrics.RequestsTotal.WithLabelValues(team.Name, "cache", "200").Inc()
				metrics.CacheHitsTotal.WithLabelValues(team.Name).Inc()
				return
			}
		}

		startTime := time.Now()
		result := router.Do(r.Context(), w, body, cfg.Upstreams, breakers)
		latency := time.Since(startTime).Seconds()

		if result.StatusCode == 502 {
			writeError(w, "all upstream providers unavailable", http.StatusBadGateway)
			slog.Error("request failed", "team", team.Name, "requestID", reqID, "latency", int(latency*1000), "statusCode", 502, "error", "all providers exhausted")
			metrics.RequestsTotal.WithLabelValues(team.Name, "", "502").Inc()
			return
		}

		if query != "" && result.StatusCode == 200 {
			// r.Context() is cancelled when the request completes — Background ensures
			// the cache write finishes even after the response has been sent to the client.
			go cache.Store(context.Background(), client, query, result.Body, cfg)
		}

		metrics.RequestsTotal.WithLabelValues(team.Name, result.Provider, strconv.Itoa(result.StatusCode)).Inc()
		metrics.TokensTotal.WithLabelValues(team.Name, result.Provider, "prompt").Add(float64(result.PromptTokens))
		metrics.TokensTotal.WithLabelValues(team.Name, result.Provider, "completion").Add(float64(result.CompletionTokens))
		metrics.RequestDuration.WithLabelValues(team.Name, result.Provider).Observe(latency)

		if result.StatusCode != 200 {
			slog.Error("request failed", "team", team.Name, "requestID", reqID, "provider", result.Provider, "latency", int(latency*1000), "statusCode", result.StatusCode, "promptTokens", result.PromptTokens, "completionTokens", result.CompletionTokens)
		} else {
			slog.Info("request logged", "team", team.Name, "requestID", reqID, "provider", result.Provider, "latency", int(latency*1000), "statusCode", result.StatusCode, "promptTokens", result.PromptTokens, "completionTokens", result.CompletionTokens)
		}
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	teamMap := make(map[string]config.Team)
	for _, team := range cfg.Teams {
		teamMap[team.KeyHash] = team
	}

	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Host + ":" + strconv.Itoa(cfg.Redis.Port),
		Password: cfg.Redis.Password,
	})

	breakers := make(map[string]*cb.CB)
	for _, upstream := range cfg.Upstreams {
		breakers[upstream.Name] = cb.NewCB(10, 10*time.Second)
	}

	if err := cache.EnsureIndex(context.Background(), client); err != nil {
		slog.Error("failed to create cache index", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", homeHandler)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("POST /v1/chat/completions", makeChatHandler(teamMap, cfg, breakers, client))

	server := &http.Server{
		Addr:    ":8030",
		Handler: mux,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("LLM Gateway running", "addr", ":8030")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}

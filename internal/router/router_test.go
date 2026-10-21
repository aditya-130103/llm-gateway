package router_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"llm-gateway/internal/cb"
	"llm-gateway/internal/config"
	"llm-gateway/internal/proxy"
	"llm-gateway/internal/router"
)

const successBody = `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`

func successServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(successBody))
	}))
}

func errorServer(t *testing.T, code int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"error":"upstream error"}`))
	}))
}

func breakers(names ...string) map[string]*cb.CB {
	m := make(map[string]*cb.CB, len(names))
	for _, n := range names {
		m[n] = cb.NewCB(3, 10*time.Second)
	}
	return m
}

func reqBody() []byte {
	return []byte(`{"model":"gpt-4.1-mini","messages":[{"role":"user","content":"hi"}]}`)
}

func TestDo_PrimarySuccess(t *testing.T) {
	s := successServer(t)
	defer s.Close()

	upstreams := []config.Upstream{{Name: "primary", BaseUrl: s.URL, Model: "gpt-4.1-mini"}}
	w := httptest.NewRecorder()

	result := router.Do(context.Background(), w, reqBody(), upstreams, breakers("primary"))

	if result.StatusCode != 200 {
		t.Errorf("StatusCode=%d, want 200", result.StatusCode)
	}
	if w.Code != 200 {
		t.Errorf("response writer code=%d, want 200", w.Code)
	}
}

func TestDo_FallbackOnOpenBreaker(t *testing.T) {
	primary := successServer(t)
	defer primary.Close()
	fallback := successServer(t)
	defer fallback.Close()

	upstreams := []config.Upstream{
		{Name: "primary", BaseUrl: primary.URL, Model: "gpt-4.1-mini"},
		{Name: "fallback", BaseUrl: fallback.URL, Model: "claude-haiku"},
	}
	bks := breakers("primary", "fallback")
	bks["primary"].RecordFailure()
	bks["primary"].RecordFailure()
	bks["primary"].RecordFailure()

	w := httptest.NewRecorder()
	result := router.Do(context.Background(), w, reqBody(), upstreams, bks)

	if result.StatusCode != 200 {
		t.Errorf("StatusCode=%d, want 200 via fallback", result.StatusCode)
	}
	if result.Provider != "fallback" {
		t.Errorf("Provider=%q, want fallback", result.Provider)
	}
}

func TestDo_FallbackOnServerError(t *testing.T) {
	primary := errorServer(t, 500)
	defer primary.Close()
	fallback := successServer(t)
	defer fallback.Close()

	upstreams := []config.Upstream{
		{Name: "primary", BaseUrl: primary.URL, Model: "gpt-4.1-mini"},
		{Name: "fallback", BaseUrl: fallback.URL, Model: "claude-haiku"},
	}

	w := httptest.NewRecorder()
	result := router.Do(context.Background(), w, reqBody(), upstreams, breakers("primary", "fallback"))

	if result.StatusCode != 200 {
		t.Errorf("StatusCode=%d, want 200 via fallback", result.StatusCode)
	}
}

func TestDo_StopsOn4xx(t *testing.T) {
	primary := errorServer(t, 401)
	defer primary.Close()
	fallback := successServer(t)
	defer fallback.Close()

	upstreams := []config.Upstream{
		{Name: "primary", BaseUrl: primary.URL},
		{Name: "fallback", BaseUrl: fallback.URL},
	}

	w := httptest.NewRecorder()
	result := router.Do(context.Background(), w, reqBody(), upstreams, breakers("primary", "fallback"))

	if result.StatusCode != 401 {
		t.Errorf("StatusCode=%d, want 401 — 4xx should stop fallback chain", result.StatusCode)
	}
}

func TestDo_AllFailReturns502(t *testing.T) {
	primary := errorServer(t, 500)
	defer primary.Close()
	fallback := errorServer(t, 503)
	defer fallback.Close()

	upstreams := []config.Upstream{
		{Name: "primary", BaseUrl: primary.URL},
		{Name: "fallback", BaseUrl: fallback.URL},
	}

	w := httptest.NewRecorder()
	result := router.Do(context.Background(), w, reqBody(), upstreams, breakers("primary", "fallback"))

	if result.StatusCode != 502 {
		t.Errorf("StatusCode=%d, want 502 when all upstreams fail", result.StatusCode)
	}
}

func TestDo_AllBreakersOpen(t *testing.T) {
	s := successServer(t)
	defer s.Close()

	upstreams := []config.Upstream{
		{Name: "primary", BaseUrl: s.URL},
		{Name: "fallback", BaseUrl: s.URL},
	}
	bks := breakers("primary", "fallback")
	bks["primary"].RecordFailure()
	bks["primary"].RecordFailure()
	bks["primary"].RecordFailure()
	bks["fallback"].RecordFailure()
	bks["fallback"].RecordFailure()
	bks["fallback"].RecordFailure()

	w := httptest.NewRecorder()
	result := router.Do(context.Background(), w, reqBody(), upstreams, bks)

	if result.StatusCode != 502 {
		t.Errorf("StatusCode=%d, want 502 when all breakers are open", result.StatusCode)
	}
}

func TestDo_ModelSwapOnFallback(t *testing.T) {
	var receivedModel string
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req proxy.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedModel = req.Model
		w.WriteHeader(200)
		_, _ = w.Write([]byte(successBody))
	}))
	defer fallback.Close()

	primary := errorServer(t, 500)
	defer primary.Close()

	upstreams := []config.Upstream{
		{Name: "primary", BaseUrl: primary.URL, Model: "gpt-4.1-mini"},
		{Name: "fallback", BaseUrl: fallback.URL, Model: "claude-haiku"},
	}

	w := httptest.NewRecorder()
	router.Do(context.Background(), w, reqBody(), upstreams, breakers("primary", "fallback"))

	if receivedModel != "claude-haiku" {
		t.Errorf("fallback received model=%q, want claude-haiku", receivedModel)
	}
}

func TestDo_EmptyUpstreams(t *testing.T) {
	w := httptest.NewRecorder()
	result := router.Do(context.Background(), w, reqBody(), nil, map[string]*cb.CB{})

	if result.StatusCode != 502 {
		t.Errorf("StatusCode=%d, want 502 for empty upstreams", result.StatusCode)
	}
}

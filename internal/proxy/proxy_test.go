package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"llm-gateway/internal/config"
	"llm-gateway/internal/proxy"
)

type mockResponse struct {
	status int
	body   string
}

func mockUpstream(t *testing.T, resp mockResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(resp.status)
		_, _ = w.Write([]byte(resp.body))
	}))
}

const validLLMBody = `{
	"choices": [{"message": {"role": "assistant", "content": "hello"}}],
	"usage": {"prompt_tokens": 10, "completion_tokens": 5}
}`

func TestForward(t *testing.T) {
	reqBody := []byte(`{"model":"gpt-4.1-mini","messages":[{"role":"user","content":"hi"}]}`)

	tests := []struct {
		name           string
		serverResp     mockResponse
		wantStatus     int
		wantPrompt     int
		wantCompletion int
		wantBodyNonNil bool
	}{
		{
			name:           "200 success with token counts",
			serverResp:     mockResponse{200, validLLMBody},
			wantStatus:     200,
			wantPrompt:     10,
			wantCompletion: 5,
			wantBodyNonNil: true,
		},
		{
			name:       "upstream 500",
			serverResp: mockResponse{500, `{"error":"internal server error"}`},
			wantStatus: 500,
		},
		{
			name:       "upstream 429 rate limited",
			serverResp: mockResponse{429, `{"error":"rate limited"}`},
			wantStatus: 429,
		},
		{
			// Proxy preserves actual status code; token counts default to 0.
			name:           "200 with invalid JSON body — status preserved, tokens zero",
			serverResp:     mockResponse{200, `not json at all`},
			wantStatus:     200,
			wantBodyNonNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := mockUpstream(t, tt.serverResp)
			defer server.Close()

			upstream := config.Upstream{
				Name:    "test",
				BaseUrl: server.URL,
				ApiKey:  "test-key",
				Model:   "test-model",
			}

			result := proxy.Forward(context.Background(), reqBody, upstream)

			if result.StatusCode != tt.wantStatus {
				t.Errorf("StatusCode=%d, want %d", result.StatusCode, tt.wantStatus)
			}
			if result.PromptTokens != tt.wantPrompt {
				t.Errorf("PromptTokens=%d, want %d", result.PromptTokens, tt.wantPrompt)
			}
			if result.CompletionTokens != tt.wantCompletion {
				t.Errorf("CompletionTokens=%d, want %d", result.CompletionTokens, tt.wantCompletion)
			}
			if tt.wantBodyNonNil && result.Body == nil {
				t.Error("expected Body to be non-nil")
			}
		})
	}
}

func TestForward_BodyStoredInResult(t *testing.T) {
	server := mockUpstream(t, mockResponse{200, validLLMBody})
	defer server.Close()

	upstream := config.Upstream{BaseUrl: server.URL, ApiKey: "k"}
	result := proxy.Forward(context.Background(), []byte(`{"model":"x","messages":[]}`), upstream)

	if result.StatusCode != 200 {
		t.Fatalf("unexpected status %d", result.StatusCode)
	}
	var v map[string]interface{}
	if err := json.Unmarshal(result.Body, &v); err != nil {
		t.Errorf("Body is not valid JSON: %v", err)
	}
}

func TestForward_UnreachableServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	upstream := config.Upstream{BaseUrl: url, ApiKey: "k"}
	result := proxy.Forward(context.Background(), []byte(`{}`), upstream)

	if result.StatusCode != 500 {
		t.Errorf("expected 500 for unreachable server, got %d", result.StatusCode)
	}
}

func TestForward_SendsAuthHeader(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(validLLMBody))
	}))
	defer server.Close()

	upstream := config.Upstream{BaseUrl: server.URL, ApiKey: "my-secret-key"}
	proxy.Forward(context.Background(), []byte(`{"model":"x","messages":[]}`), upstream)

	if receivedAuth != "Bearer my-secret-key" {
		t.Errorf("Authorization=%q, want Bearer my-secret-key", receivedAuth)
	}
}

func TestForward_InvalidURL(t *testing.T) {
	upstream := config.Upstream{BaseUrl: "http://invalid\x00host", ApiKey: "k"}
	result := proxy.Forward(context.Background(), []byte(`{}`), upstream)
	if result.StatusCode != 500 {
		t.Errorf("expected 500 for invalid URL, got %d", result.StatusCode)
	}
}

func TestForward_ConnectionDropMidBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("server does not support hijacking")
			return
		}
		conn, _, _ := hj.Hijack()
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\npartial"))
		_ = conn.Close()
	}))
	defer server.Close()

	upstream := config.Upstream{BaseUrl: server.URL, ApiKey: "k"}
	result := proxy.Forward(context.Background(), []byte(`{"model":"x","messages":[]}`), upstream)
	if result.StatusCode != 500 {
		t.Errorf("expected 500 when connection drops mid-body, got %d", result.StatusCode)
	}
}

func BenchmarkForward(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(validLLMBody))
	}))
	defer server.Close()

	upstream := config.Upstream{BaseUrl: server.URL, ApiKey: "k"}
	body := []byte(`{"model":"gpt-4.1-mini","messages":[{"role":"user","content":"hi"}]}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		proxy.Forward(context.Background(), body, upstream)
	}
}

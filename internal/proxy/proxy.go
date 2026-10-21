package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"llm-gateway/internal/config"
)

type Result struct {
	PromptTokens     int
	CompletionTokens int
	StatusCode       int
	Provider         string
	Body             []byte
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type upstreamUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type upstreamResponse struct {
	Usage upstreamUsage `json:"usage"`
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func Forward(ctx context.Context, body []byte, upstream config.Upstream) Result {
	request, err := http.NewRequestWithContext(ctx, "POST", upstream.BaseUrl+"/chat/completions", bytes.NewBuffer(body))
	if err != nil {
		return Result{StatusCode: 500}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+upstream.ApiKey)

	response, err := httpClient.Do(request)
	if err != nil {
		return Result{StatusCode: 500}
	}
	defer func() { _ = response.Body.Close() }()

	statusCode := response.StatusCode

	body, err = io.ReadAll(response.Body)
	if err != nil {
		return Result{StatusCode: 500}
	}

	var responseBody upstreamResponse
	_ = json.Unmarshal(body, &responseBody) // best-effort; missing fields default to 0

	return Result{
		PromptTokens:     responseBody.Usage.PromptTokens,
		CompletionTokens: responseBody.Usage.CompletionTokens,
		StatusCode:       statusCode,
		Body:             body,
	}
}

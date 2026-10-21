package cache

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
	"llm-gateway/internal/config"
)

const embeddingDim = 1536

var embedClient = &http.Client{Timeout: 30 * time.Second}

type embedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

// vecToBytes encodes a float32 slice as IEEE 754 little-endian bytes.
// Redis FLOAT32 vector fields require this exact wire format.
func vecToBytes(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

func embed(ctx context.Context, embedding config.Embedding, text string) ([]float32, error) {
	body, err := json.Marshal(map[string]interface{}{
		"input": text,
		"model": embedding.Model,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", embedding.BaseUrl, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+embedding.ApiKey)
	resp, err := embedClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	embedResponse := embedResponse{}
	err = json.Unmarshal(body, &embedResponse)
	if err != nil {
		return nil, err
	}

	if len(embedResponse.Data) == 0 {
		return nil, fmt.Errorf("embedding API returned empty data")
	}
	vec := make([]float32, len(embedResponse.Data[0].Embedding))
	for i, v := range embedResponse.Data[0].Embedding {
		vec[i] = float32(v)
	}
	return vec, nil
}

func EnsureIndex(ctx context.Context, redisClient *redis.Client) error {
	// Swallow "Index already exists" — gateway restarts must not fail if the index
	// was created on a previous run.
	cmd := redisClient.Do(ctx, "FT.CREATE", "idx:cache", "ON", "HASH",
	"PREFIX", "1", "cache:",
	"SCHEMA",
	"embedding", "VECTOR", "HNSW", "6", "TYPE", "FLOAT32", "DIM", strconv.Itoa(embeddingDim), "DISTANCE_METRIC", "COSINE",
	"answer", "TEXT").Err()
	if cmd != nil && !strings.Contains(cmd.Error(), "Index already exists") {
		return cmd
	}
	return nil
}

func Store(ctx context.Context, client *redis.Client, query string, body []byte, cfg config.Config) {
	vec, err := embed(ctx, cfg.Embedding, query)
	if err != nil {
		slog.Warn("cache store: embed failed", "error", err)
		return
	}
	key := fmt.Sprintf("cache:%d", time.Now().UnixNano())
	cmd := client.HSet(ctx, key, "embedding", vecToBytes(vec), "answer", body)
	if cmd.Err() != nil {
		slog.Warn("cache store: hset failed", "error", cmd.Err())
		return
	}
	if err = client.Expire(ctx, key, time.Duration(cfg.Embedding.TTLSeconds)*time.Second).Err(); err != nil {
		slog.Warn("cache store: expire failed", "error", err)
	}
}

func Lookup(ctx context.Context, client *redis.Client, query string, cfg config.Config) ([]byte, bool) {
	vec, err := embed(ctx, cfg.Embedding, query)
	if err != nil {
		return nil, false
	}
	cmd := client.Do(ctx, "FT.SEARCH", "idx:cache",
		"*=>[KNN 1 @embedding $vec AS score]",
		"PARAMS", "2", "vec", vecToBytes(vec),
		"SORTBY", "score", "ASC",
		"LIMIT", "0", "1", 
		"DIALECT", "2")
	res, err := cmd.Result()
	if err != nil {
		return nil, false
	}

	resMap, ok := res.(map[interface{}]interface{})
	if !ok {
		return nil, false
	}
	totalResults, ok := resMap["total_results"].(int64)
	if !ok || totalResults == 0 {
		return nil, false
	}

	results, ok := resMap["results"].([]interface{})
	if !ok || len(results) == 0 {
		return nil, false
	}
	firstResult, ok := results[0].(map[interface{}]interface{})
	if !ok {
		return nil, false
	}
	extraAttrs, ok := firstResult["extra_attributes"].(map[interface{}]interface{})
	if !ok {
		return nil, false
	}
	scoreStr, ok := extraAttrs["score"].(string)
	if !ok {
		return nil, false
	}
	answerStr, ok := extraAttrs["answer"].(string)
	if !ok {
		return nil, false
	}

	score, err := strconv.ParseFloat(scoreStr, 64)
	if err != nil {
		return nil, false
	}
	answer := []byte(answerStr)

	// Redis returns cosine distance (1 - similarity), not similarity.
	// Threshold 0.85 similarity → max allowed distance 0.15.
	if score <= (1 - cfg.Embedding.SimilarityThreshold) {
		return answer, true
	}
	return nil, false
}
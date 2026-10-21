package router

import (
	"context"
	"encoding/json"
	"net/http"

	"llm-gateway/internal/cb"
	"llm-gateway/internal/config"
	"llm-gateway/internal/proxy"
)

func Do(ctx context.Context, w http.ResponseWriter, body []byte, upstreams []config.Upstream, breakers map[string]*cb.CB) proxy.Result {
	for i, upstream := range upstreams {
		if !breakers[upstream.Name].Allow() {
			continue
		}

		sendBody := body
		if i > 0 {
			var req proxy.Request
			if err := json.Unmarshal(body, &req); err == nil {
				req.Model = upstream.Model
				if swapped, err := json.Marshal(req); err == nil {
					sendBody = swapped
				}
			}
		}

		result := proxy.Forward(ctx, sendBody, upstream)
		result.Provider = upstream.Name
		if result.StatusCode == 200 {
			breakers[upstream.Name].RecordSuccess()
			w.WriteHeader(result.StatusCode)
			_, _ = w.Write(result.Body)
			return result
		} else if result.StatusCode >= 400 && result.StatusCode < 500 {
			w.WriteHeader(result.StatusCode)
			_, _ = w.Write(result.Body)
			return result
		} else if result.StatusCode >= 500 {
			breakers[upstream.Name].RecordFailure()
		}
	}
	return proxy.Result{StatusCode: 502}
}

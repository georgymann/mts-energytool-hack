package consultation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"energytool/backend/internal/domain"
)

type Client interface {
	Respond(context.Context, domain.ConsultationRequest) (domain.ConsultationResult, error)
}

type HTTPClient struct {
	baseURL string
	client  *http.Client
}

func NewHTTPClient(baseURL string, timeout time.Duration) (*HTTPClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, errors.New("ML service URL is required")
	}
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("ML service URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	if timeout <= 0 {
		return nil, errors.New("ML timeout must be positive")
	}
	return &HTTPClient{baseURL: baseURL, client: &http.Client{Timeout: timeout}}, nil
}

func (c *HTTPClient) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health/ready", nil)
	if err != nil {
		return errors.New("create readiness request")
	}
	response, err := c.client.Do(req)
	if err != nil {
		return errors.New("consultation service unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("consultation service is not ready")
	}
	return nil
}

func (c *HTTPClient) Respond(ctx context.Context, request domain.ConsultationRequest) (domain.ConsultationResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return domain.ConsultationResult{}, fmt.Errorf("encode consultation request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/respond", bytes.NewReader(body))
	if err != nil {
		return domain.ConsultationResult{}, errors.New("create consultation request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return domain.ConsultationResult{}, errors.New("consultation service unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.ConsultationResult{}, fmt.Errorf("consultation service returned HTTP %d", response.StatusCode)
	}
	const maxResponseBytes = 2 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return domain.ConsultationResult{}, errors.New("consultation response is unreadable or too large")
	}
	var result domain.ConsultationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return domain.ConsultationResult{}, errors.New("invalid consultation response")
	}
	if strings.TrimSpace(result.Answer) == "" || result.Lead.SessionID != request.SessionID {
		return domain.ConsultationResult{}, errors.New("consultation response failed validation")
	}
	switch result.Status {
	case "answered", "clarification", "knowledge_gap", "out_of_scope", "technical_error":
	default:
		return domain.ConsultationResult{}, errors.New("consultation response has an unsupported status")
	}
	if result.SchemaVersion != "1.0" {
		return domain.ConsultationResult{}, errors.New("unsupported consultation schema version")
	}
	if result.Sources == nil {
		result.Sources = []map[string]interface{}{}
	}
	if result.Summary == nil {
		result.Summary = map[string]interface{}{}
	}
	if result.Debug == nil {
		result.Debug = map[string]interface{}{}
	}
	return result, nil
}

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/chenhg5/agentflow/internal/hubapi"
)

// Sender delivers a batch of reports to a Hub.
type Sender interface {
	Send(ctx context.Context, reports []hubapi.RunReport) (hubapi.IngestResult, error)
}

// HTTPSender posts batches to the Hub's ingest endpoint.
type HTTPSender struct {
	URL    string
	Token  string
	Client *http.Client
}

// NewHTTPSender builds a sender for config.
func NewHTTPSender(config Config) *HTTPSender {
	return &HTTPSender{URL: config.URL, Token: config.Token, Client: &http.Client{Timeout: 10 * time.Second}}
}

// Send posts one batch.
func (s *HTTPSender) Send(ctx context.Context, reports []hubapi.RunReport) (hubapi.IngestResult, error) {
	var result hubapi.IngestResult
	body, err := json.Marshal(hubapi.Batch{Reports: reports})
	if err != nil {
		return result, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+hubapi.IngestPath, bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	request.Header.Set("Content-Type", "application/json")
	if s.Token != "" {
		request.Header.Set("Authorization", "Bearer "+s.Token)
	}
	response, err := s.Client.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("hub answered %d: %s", response.StatusCode, bytes.TrimSpace(data))
	}
	err = json.Unmarshal(data, &result)
	return result, err
}

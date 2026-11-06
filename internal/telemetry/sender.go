package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// Commands fetches the Hub's pending commands for machine.
func (s *HTTPSender) Commands(ctx context.Context, machine string) ([]hubapi.Command, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+hubapi.CommandsPath+"?machine="+url.QueryEscape(machine), nil)
	if err != nil {
		return nil, err
	}
	var list hubapi.CommandList
	if err := s.do(request, &list); err != nil {
		return nil, err
	}
	return list.Commands, nil
}

// Answer reports what happened to a command.
func (s *HTTPSender) Answer(ctx context.Context, machine, id string, result hubapi.CommandResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+hubapi.CommandsPath+"/"+url.PathEscape(id)+"?machine="+url.QueryEscape(machine), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	return s.do(request, nil)
}

func (s *HTTPSender) do(request *http.Request, into any) error {
	if s.Token != "" {
		request.Header.Set("Authorization", "Bearer "+s.Token)
	}
	response, err := s.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("hub answered %d: %s", response.StatusCode, bytes.TrimSpace(data))
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(data, into)
}

package trigger

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

type ServeOptions struct {
	Host    string
	Port    int
	Workers int
}

func (source WebhookSource) Handler(dispatcher *Dispatcher) http.Handler {
	return webhookHandler(dispatcher, Config{Triggers: []Definition{source.Definition}})
}

func Serve(ctx context.Context, root, cwd string, options ServeOptions) error {
	config, err := LoadConfig(root)
	if err != nil {
		return err
	}
	release, err := acquireWorkspaceLock(ctx, root, "scheduler.lock", false)
	if errors.Is(err, errWorkspaceBusy) {
		return fmt.Errorf("%w: %s", ErrServiceBusy, root)
	}
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runtime, err := NewRuntime(root, cwd, options.Workers)
	if err != nil {
		return err
	}
	dispatcher := &Dispatcher{Root: root, Config: config, Starter: runtime}
	var listener net.Listener
	for _, definition := range config.Triggers {
		if definition.Enabled && definition.Source.Kind == "webhook" {
			host := options.Host
			if host == "" {
				host = "127.0.0.1"
			}
			listener, err = net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(options.Port)))
			if err != nil {
				return err
			}
			defer listener.Close()
			break
		}
	}
	// Reserve the listener before recovering queued jobs or starting timers.
	// A port conflict must not launch work from a service that failed to start.
	runtime.StartWorkers(ctx)
	cronStop, err := startCron(ctx, config, dispatcher)
	if err != nil {
		return err
	}
	defer func() {
		cancel()
		cronStop()
	}()

	if listener == nil {
		<-ctx.Done()
		return nil
	}
	server := &http.Server{Handler: webhookHandler(dispatcher, config)}
	fmt.Fprintf(os.Stderr, "trigger webhook listener: http://%s\n", listener.Addr())
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func startCron(ctx context.Context, config Config, dispatcher *Dispatcher) (func(), error) {
	schedulers := []*cron.Cron{}
	stop := func() {
		for _, scheduler := range schedulers {
			<-scheduler.Stop().Done()
		}
	}
	for _, definition := range config.Triggers {
		if !definition.Enabled || definition.Source.Kind != "cron" {
			continue
		}
		source := CronSource{Definition: definition}
		scheduler, err := source.scheduler(func(event Event) {
			if _, err := dispatcher.Dispatch(ctx, event); err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "trigger %s dispatch failed: %v\n", event.TriggerID, err)
			}
		})
		if err != nil {
			stop()
			return nil, err
		}
		scheduler.Start()
		schedulers = append(schedulers, scheduler)
	}
	return stop, nil
}

func (source CronSource) scheduler(emit func(Event)) (*cron.Cron, error) {
	location := time.UTC
	if source.Definition.Source.Timezone != "" {
		loaded, err := time.LoadLocation(source.Definition.Source.Timezone)
		if err != nil {
			return nil, fmt.Errorf("trigger %s timezone: %w", source.ID(), err)
		}
		location = loaded
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	scheduler := cron.New(cron.WithLocation(location), cron.WithParser(parser))
	if _, err := scheduler.AddFunc(source.Definition.Source.Expression, func() {
		now := time.Now().UTC()
		emit(Event{TriggerID: source.ID(), SourceKind: source.Kind(), DeliveryID: source.ID() + ":" + now.Format(time.RFC3339), ReceivedAt: now, ScheduledAt: &now, Attributes: map[string]string{}})
	}); err != nil {
		return nil, fmt.Errorf("trigger %s cron expression: %w", source.ID(), err)
	}
	return scheduler, nil
}

func (source CronSource) Serve(ctx context.Context, sink EventSink) error {
	scheduler, err := source.scheduler(func(event Event) { _ = sink(context.Background(), event) })
	if err != nil {
		return err
	}
	scheduler.Start()
	<-ctx.Done()
	<-scheduler.Stop().Done()
	return nil
}

func webhookHandler(dispatcher *Dispatcher, config Config) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path := strings.TrimSuffix(request.URL.Path, "/")
		var definition Definition
		found := false
		for _, item := range config.Triggers {
			if item.Enabled && item.Source.Kind == "webhook" && strings.TrimSuffix(item.Source.Path, "/") == path {
				definition = item
				found = true
				break
			}
		}
		if !found {
			http.Error(writer, "trigger not found", http.StatusNotFound)
			return
		}
		method := definition.Source.Method
		if method == "" {
			method = http.MethodPost
		}
		if request.Method != method {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		maxBody := definition.Source.MaxBodyBytes
		if maxBody <= 0 {
			maxBody = 1 << 20
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maxBody+1))
		if err != nil {
			http.Error(writer, "cannot read request body", http.StatusBadRequest)
			return
		}
		if int64(len(body)) > maxBody {
			http.Error(writer, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		if err := verifyWebhook(definition.Source.Auth, definition.Source.SignatureHeader, body, request.Header); err != nil {
			http.Error(writer, err.Error(), http.StatusUnauthorized)
			return
		}
		deliveryID := request.Header.Get(definition.Source.DeliveryIDHeader)
		if definition.Source.DeliveryIDHeader == "" {
			deliveryID = request.Header.Get("X-Delivery-ID")
		}
		if deliveryID == "" {
			deliveryID = fmt.Sprintf("webhook-%d", time.Now().UnixNano())
		}
		headers := map[string]string{}
		for key, values := range request.Header {
			if len(values) > 0 {
				headers[key] = values[0]
			}
		}
		event := Event{TriggerID: definition.ID, SourceKind: "webhook", DeliveryID: deliveryID, ReceivedAt: time.Now().UTC(), Payload: body, Headers: headers, Attributes: map[string]string{}}
		result, err := dispatcher.Dispatch(request.Context(), event)
		if err != nil {
			status := http.StatusUnprocessableEntity
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "disabled") {
				status = http.StatusNotFound
			}
			http.Error(writer, err.Error(), status)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(writer).Encode(result)
	})
}

func verifyWebhook(auth *AuthConfig, configuredSignatureHeader string, body []byte, headers http.Header) error {
	if auth == nil || auth.Kind == "" {
		return nil
	}
	switch strings.ToLower(auth.Kind) {
	case "hmac-sha256":
		secret := os.Getenv(auth.SecretEnv)
		if secret == "" {
			return fmt.Errorf("webhook secret is not configured")
		}
		header := configuredSignatureHeader
		if header == "" {
			header = "X-Hub-Signature-256"
		}
		provided := headers.Get(header)
		provided = strings.TrimPrefix(provided, "sha256=")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(strings.ToLower(provided)), []byte(expected)) {
			return fmt.Errorf("invalid webhook signature")
		}
		return nil
	case "bearer":
		token := os.Getenv(auth.TokenEnv)
		if token == "" || headers.Get("Authorization") != "Bearer "+token {
			return fmt.Errorf("invalid bearer token")
		}
		return nil
	default:
		return fmt.Errorf("unsupported webhook auth kind: %s", auth.Kind)
	}
}

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

	"github.com/chenhg5/agentflow/internal/queue"
	"github.com/chenhg5/agentflow/internal/store"
)

type ServeOptions struct {
	Host    string
	Port    int
	Workers int
}

func (source WebhookSource) Handler(dispatcher *Dispatcher) http.Handler {
	return webhookHandler(dispatcher, Config{Triggers: []Definition{source.Definition}})
}

func Serve(ctx context.Context, root, cwd string, options ServeOptions) (serveErr error) {
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
	if options.Workers < 1 {
		options.Workers = 1
	}
	worker, err := queue.Start(ctx, root, cwd, options.Workers, true)
	if err != nil && !errors.Is(err, queue.ErrWorkerBusy) {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	// An existing worker owns admission. This scheduler only publishes requests.
	if worker != nil {
		defer func() {
			cancel()
			if err := worker.Wait(); serveErr == nil {
				serveErr = err
			}
		}()
		// An owned worker's fatal persistence error must stop the service and
		// reach stderr, rather than leaving a scheduler with no admission owner.
		go func() { _ = worker.Wait(); cancel() }()
	}
	cronStop, err := startCron(ctx, dispatcher)
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

func startCron(ctx context.Context, dispatcher *Dispatcher) (func(), error) {
	cronCtx, cancel := context.WithCancel(ctx)
	runner := &CronRunner{Dispatcher: dispatcher}
	if err := runner.Tick(cronCtx, true); err != nil {
		cancel()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-cronCtx.Done():
				return
			case <-ticker.C:
				if err := runner.Tick(cronCtx, false); err != nil && cronCtx.Err() == nil {
					fmt.Fprintf(os.Stderr, "cron dispatch failed: %v\n", err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

// Serve is an ephemeral source port. The CLI uses CronRunner's durable cursor.
func (source CronSource) Serve(ctx context.Context, sink EventSink) error {
	schedule, err := cronSchedule(source.Definition)
	if err != nil {
		return err
	}
	for {
		planned := schedule.Next(time.Now())
		if planned.IsZero() {
			return fmt.Errorf("cron expression has no next occurrence")
		}
		timer := time.NewTimer(time.Until(planned))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
			zone := source.Definition.Source.Timezone
			if zone == "" {
				zone = "UTC"
			}
			event := Event{TriggerID: source.ID(), SourceKind: "cron", ScheduledAt: &planned, ReceivedAt: time.Now().UTC(), DeliveryID: OccurrenceID(source.ID(), planned), Timezone: zone}
			if err := sink(ctx, event); err != nil {
				return err
			}
		}
	}
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
			if errors.Is(err, store.ErrIdempotencyConflict) {
				status = http.StatusConflict
			}
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

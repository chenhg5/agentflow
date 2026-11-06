// Package hub wires the Hub together: a store, the use cases, the JSON API and
// the pages, behind one HTTP server. Each piece is replaceable on its own; the
// store is chosen by a DSN so another database can be added without touching
// the rest.
package hub

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/api"
	"github.com/chenhg5/agentflow/internal/hub/app"
	"github.com/chenhg5/agentflow/internal/hub/store/memory"
	"github.com/chenhg5/agentflow/internal/hub/store/sqlite"
	"github.com/chenhg5/agentflow/internal/hub/ui"
)

// Options configures a Hub server.
type Options struct {
	Host string
	Port int
	// Store is a DSN: "memory:" or "sqlite:<path>" (a bare path means sqlite).
	Store string
	// IngestToken authenticates runners. Required unless the Hub only listens on loopback.
	IngestToken string
	// ViewToken protects the pages and read API. Empty means open on loopback,
	// and the ingest token everywhere else.
	ViewToken string
	Version   string
	Logf      func(format string, args ...any)
	// Retain keeps finished runs this long; zero keeps them forever.
	Retain time.Duration
}

// OpenStore opens the store a DSN names.
func OpenStore(ctx context.Context, dsn string) (app.Store, error) {
	kind, rest, found := strings.Cut(dsn, ":")
	if !found || len(kind) == 1 { // a bare path, or a Windows drive letter
		kind, rest = "sqlite", dsn
	}
	switch kind {
	case "memory":
		return memory.New(), nil
	case "sqlite":
		if rest == "" {
			return nil, errors.New("sqlite store needs a path, e.g. sqlite:hub.db")
		}
		if err := os.MkdirAll(filepath.Dir(rest), 0o755); err != nil {
			return nil, err
		}
		return sqlite.Open(ctx, rest)
	default:
		return nil, fmt.Errorf("unknown hub store %q: use sqlite:<path> or memory:", kind)
	}
}

// Server is a running Hub.
type Server struct {
	retain   time.Duration
	store    app.Store
	http     *http.Server
	listener net.Listener
	logf     func(format string, args ...any)
}

// New opens the store and builds the handler, ready to Serve.
func New(ctx context.Context, options Options) (*Server, error) {
	host := options.Host
	if host == "" {
		host = "127.0.0.1"
	}
	loopback := isLoopback(host)
	if options.IngestToken == "" && !loopback {
		return nil, errors.New("a hub that listens beyond this machine needs a token: set AGENTFLOW_HUB_TOKEN")
	}
	store, err := OpenStore(ctx, options.Store)
	if err != nil {
		return nil, err
	}
	handler, err := Handler(store, options, loopback)
	if err != nil {
		store.Close()
		return nil, err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(options.Port)))
	if err != nil {
		store.Close()
		return nil, err
	}
	logf := options.Logf
	if logf == nil {
		logf = log.Printf
	}
	return &Server{retain: options.Retain, store: store, listener: listener, logf: logf, http: &http.Server{
		Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, WriteTimeout: time.Minute, IdleTimeout: 2 * time.Minute,
	}}, nil
}

// Handler builds the Hub's routes over store. loopback says whether only this
// machine can reach the server, which decides the default access rules.
func Handler(store app.Store, options Options, loopback bool) (http.Handler, error) {
	ingest := app.Ingest{Store: store}
	query := app.Query{Store: store}
	commands := app.Commands{Store: store}
	ingestGuard, viewGuard := api.Guard(api.Open), api.Guard(api.Open)
	if options.IngestToken != "" {
		ingestGuard = api.BearerToken(options.IngestToken)
	}
	switch {
	case options.ViewToken != "":
		viewGuard = api.ViewerToken(options.ViewToken)
	case !loopback:
		viewGuard = api.ViewerToken(options.IngestToken)
	}
	pages, err := ui.New(query, commands, options.Version)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	api.API{Ingest: ingest, Query: query, Commands: commands}.Register(mux, ingestGuard, viewGuard)
	pages.Register(mux, viewGuard)
	return securityHeaders(mux), nil
}

// URL is where the Hub can be reached.
func (s *Server) URL() string { return "http://" + s.listener.Addr().String() }

// Serve runs until ctx is done, then shuts down gracefully.
func (s *Server) Serve(ctx context.Context) error {
	defer s.store.Close()
	go app.Retention{Store: s.store, Keep: s.retain, Logf: s.logf}.Every(ctx, time.Hour)
	errs := make(chan error, 1)
	go func() { errs <- s.http.Serve(s.listener) }()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.http.Shutdown(shutdown)
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "same-origin")
		header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://esm.sh; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com https://unpkg.com; font-src https://fonts.gstatic.com; img-src 'self' data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

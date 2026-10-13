package trigger

import "context"

// EventSink is the only callback a source needs. Sources discover and
// authenticate events; dispatching and run creation stay in the application
// layer.
type EventSink func(context.Context, Event) error

// Source is the stable port shared by cron, webhook, and future event
// adapters. It deliberately exposes no workflow or engine details.
type Source interface {
	ID() string
	Kind() string
}

type ServingSource interface {
	Source
	Serve(context.Context, EventSink) error
}

type CronSource struct{ Definition Definition }

func (source CronSource) ID() string   { return source.Definition.ID }
func (source CronSource) Kind() string { return "cron" }

// WebhookSource is used by the HTTP adapter to route one configured endpoint.
// The shared server keeps one listener for all webhook definitions.
type WebhookSource struct{ Definition Definition }

func (source WebhookSource) ID() string   { return source.Definition.ID }
func (source WebhookSource) Kind() string { return "webhook" }

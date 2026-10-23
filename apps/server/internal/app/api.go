package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"connectrpc.com/vanguard"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/event/v1/eventv1connect"
)

// usageFlushInterval is how often the API role writes the last use of API
// keys (lago's clock api_keys_track_usage runs hourly). The uses are
// collected in the API process, so the API process writes them, rather
// than the clock job of a worker.
const usageFlushInterval = time.Hour

// finalFlushTimeout bounds the flush of API key uses at shutdown.
const finalFlushTimeout = 10 * time.Second

// restPrefix is where the REST transcoding of the public RPCs is served
// (google.api.http annotations).
const restPrefix = "/v1/"

// api is the API role: the Connect handlers (Connect, gRPC and gRPC-Web
// at /<package>.<Service>/<Method>) and their REST transcoding by
// Vanguard under /v1/.
type api struct {
	usage *auth.UsageTracker
	tx    port.Transactor
	log   *slog.Logger
}

// apiDeps are what the API role serves.
type apiDeps struct {
	tx       port.Transactor
	ingest   *ingest.Service
	maxBytes int64
	log      *slog.Logger
	tp       trace.TracerProvider
	mp       metric.MeterProvider
}

// newAPI mounts the API on mux. Every RPC passes through the
// interceptors, outermost first: telemetry, panic recovery,
// authentication and authorization (the RPC's access option), request
// validation (protovalidate).
func newAPI(mux *http.ServeMux, d apiDeps) (*api, error) {
	a := &api{usage: auth.NewUsageTracker(), tx: d.tx, log: d.log}
	keys := auth.NewAPIKeys(auth.Deps{
		Tx:     d.tx,
		Clock:  core.SystemClock{},
		IDs:    core.UUIDv7Generator{},
		Rand:   rand.Reader,
		Logger: d.log,
	}, a.usage)
	validator, err := protovalidate.New()
	if err != nil {
		return nil, fmt.Errorf("api: validator: %w", err)
	}
	telemetry, err := rpc.Telemetry(d.tp, d.mp)
	if err != nil {
		return nil, err
	}
	server := connect.NewServer(telemetry, rpc.Recover(), rpc.Auth(keys, auth.NewAuthorizer(d.tx)), rpc.Validate(validator))
	eventv1connect.RegisterEventServiceHandler(server, rpc.NewEvents(d.ingest))

	// JSON uses the proto field names (snake_case) and keeps the numbers
	// of event properties exact, on both transports.
	connecthttp.Mount(mux, server,
		connecthttp.WithCodecs(connectproto.NewBinaryCodec(), rpc.NewJSONCodec()),
		connecthttp.WithReadMaxBytes(int(d.maxBytes)),
	)
	rest := http.NewServeMux()
	if err := vanguard.Mount(rest, server,
		vanguard.WithCodec(func(res connectproto.TypeResolver) vanguard.RESTCodec {
			return rpc.NewRESTCodec(res)
		}),
		vanguard.WithMaxReadBytes(d.maxBytes),
	); err != nil {
		return nil, fmt.Errorf("api: rest: %w", err)
	}
	mux.Handle(restPrefix, rest)
	return a, nil
}

// runUsageFlush writes the API key uses every [usageFlushInterval] until
// ctx ends, then once more.
func (a *api) runUsageFlush(ctx context.Context) error {
	t := time.NewTicker(usageFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalFlushTimeout)
			a.flushUsage(fctx)
			cancel()
			return nil
		case <-t.C:
			a.flushUsage(ctx)
		}
	}
}

func (a *api) flushUsage(ctx context.Context) {
	if a.usage.Pending() == 0 {
		return
	}
	if err := a.usage.Flush(ctx, a.tx); err != nil {
		a.log.WarnContext(ctx, "api: track api key usage", "error", err)
	}
}

package rpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/idempotency"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// IdempotencyHeader is the request header carrying an idempotency key.
const IdempotencyHeader = "Idempotency-Key"

// Reasons of the idempotency errors.
const (
	// ReasonIdempotencyKeyReused: the key was used with another RPC or
	// another request.
	ReasonIdempotencyKeyReused = "idempotency_key_reused"
	// ReasonIdempotencyKeyInProgress: a request with the key is running.
	ReasonIdempotencyKeyInProgress = "idempotency_key_in_progress"
)

// Idempotency makes the unary RPCs with side effects idempotent per
// organization and Idempotency-Key header for [idempotency.TTL]
// (architecture §4.5). It runs after [Auth], so the organization is
// known, and after [Validate]. The first request with a key claims it and
// runs; its successful response is stored and replayed to every retry
// with the same RPC and the same request message. A retry with another
// RPC or request fails with InvalidArgument, one sent while the first
// still runs with Aborted. A failed request releases the key, so it can
// be retried. Requests without the header, NO_SIDE_EFFECTS RPCs,
// streaming RPCs and anonymous RPCs are not affected. The client header is
// Tanvo's: lago keeps idempotency records only as internal keys of a few
// services, which does not protect clients retrying a lost response.
func Idempotency(tx port.Transactor, clock core.Clock) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			info, _ := connect.CallInfoForServerContext(ctx)
			md, ok := spec.Schema.(protoreflect.MethodDescriptor)
			if info == nil || !ok || spec.StreamType != connect.StreamTypeUnary || idempotencyLevel(md) == descriptorpb.MethodOptions_NO_SIDE_EFFECTS {
				return next(ctx, spec, stream)
			}
			key := info.RequestHeader().Get(IdempotencyHeader)
			p, authed := Principal(ctx)
			if key == "" || !authed || p.OrganizationID.IsZero() {
				return next(ctx, spec, stream)
			}
			if len(key) > idempotency.MaxKeyLen {
				return InvalidArgument("invalid idempotency key", []FieldViolation{{
					Field: IdempotencyHeader, Reason: core.CodeTooLong,
					Description: fmt.Sprintf("must be at most %d bytes", idempotency.MaxKeyLen),
				}})
			}
			k := &keyed{tx: tx, clock: clock, org: p.OrganizationID, key: key, procedure: spec.Procedure}
			return k.serve(ctx, spec, md, stream, next)
		}
	}
}

// keyed serves one request with an idempotency key.
type keyed struct {
	tx        port.Transactor
	clock     core.Clock
	org       core.OrganizationID
	key       string
	procedure string
}

func (k *keyed) serve(ctx context.Context, spec connect.Spec, md protoreflect.MethodDescriptor, stream connect.ServerStream, next connect.ServerFunc) error {
	req, err := newMessage(md.Input())
	if err != nil {
		return err
	}
	if err := stream.Receive(req); err != nil {
		return err
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return fmt.Errorf("rpc: idempotency: encode request: %w", err)
	}
	sum := sha256.Sum256(raw)
	hash := sum[:]
	replay, err := k.claim(ctx, hash)
	if err != nil {
		return err
	}
	if replay != nil {
		resp, err := newMessage(md.Output())
		if err != nil {
			return err
		}
		if err := proto.Unmarshal(replay, resp); err != nil {
			return Error(ctx, fmt.Errorf("decode stored response: %w", err))
		}
		return stream.Send(resp)
	}
	s := &recordingStream{ServerStream: stream, req: req}
	if err := next(ctx, spec, s); err != nil {
		k.release(ctx)
		return err
	}
	if s.resp == nil {
		k.release(ctx)
		return nil
	}
	out, err := proto.MarshalOptions{Deterministic: true}.Marshal(s.resp)
	if err != nil {
		k.release(ctx)
		return fmt.Errorf("rpc: idempotency: encode response: %w", err)
	}
	err = k.tx.InTx(context.WithoutCancel(ctx), func(ctx context.Context, r port.Repos) error {
		rec, err := r.IdempotencyKeys.Get(ctx, k.org, k.key)
		if err != nil {
			return err
		}
		return r.IdempotencyKeys.Complete(ctx, k.org, k.key, out, rec.CreatedAt.Add(idempotency.TTL))
	})
	if err != nil {
		// The response was sent; a retry runs the request again.
		slog.WarnContext(ctx, "rpc: idempotency: store response", "error", err)
	}
	return nil
}

// claim claims the key for a request whose message hashes to hash, or
// returns the stored response of the request that holds it.
func (k *keyed) claim(ctx context.Context, hash []byte) ([]byte, error) {
	var replay []byte
	err := k.tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		now := k.clock.Now().UTC()
		for range 2 {
			rec, err := r.IdempotencyKeys.Get(ctx, k.org, k.key)
			switch {
			case errors.Is(err, core.ErrNotFound):
			case err != nil:
				return err
			case !rec.Expired(now):
				if rec.Procedure != k.procedure || !bytes.Equal(rec.RequestHash, hash) {
					return newError(connect.CodeInvalidArgument, ReasonIdempotencyKeyReused,
						"the idempotency key was used with another request")
				}
				if rec.Pending() {
					return newError(connect.CodeAborted, ReasonIdempotencyKeyInProgress,
						"a request with this idempotency key is in progress; retry later")
				}
				replay = rec.Response
				return nil
			}
			ok, err := r.IdempotencyKeys.Claim(ctx, &idempotency.Record{
				OrganizationID: k.org, Key: k.key, Procedure: k.procedure, RequestHash: hash,
				CreatedAt: now, ExpiresAt: now.Add(idempotency.PendingTTL),
			})
			if err != nil || ok {
				return err
			}
			// Another request claimed the key in between: read it again.
		}
		return newError(connect.CodeAborted, ReasonIdempotencyKeyInProgress,
			"a request with this idempotency key is in progress; retry later")
	})
	if err != nil {
		return nil, Error(ctx, err)
	}
	return replay, nil
}

// release frees the key of a failed request.
func (k *keyed) release(ctx context.Context) {
	err := k.tx.InTx(context.WithoutCancel(ctx), func(ctx context.Context, r port.Repos) error {
		return r.IdempotencyKeys.Release(ctx, k.org, k.key)
	})
	if err != nil {
		slog.WarnContext(ctx, "rpc: idempotency: release key", "error", err)
	}
}

// recordingStream hands the received request to the handler and keeps
// the response it sends.
type recordingStream struct {
	connect.ServerStream
	req  proto.Message
	resp proto.Message
}

func (s *recordingStream) Receive(msg any) error {
	m, ok := msg.(proto.Message)
	if !ok || s.req == nil {
		return s.ServerStream.Receive(msg)
	}
	proto.Reset(m)
	proto.Merge(m, s.req)
	s.req = nil
	return nil
}

func (s *recordingStream) Send(msg any) error {
	if m, ok := msg.(proto.Message); ok {
		s.resp = m
	}
	return s.ServerStream.Send(msg)
}

// newMessage returns a new message of the generated type of d.
func newMessage(d protoreflect.MessageDescriptor) (proto.Message, error) {
	t, err := protoregistry.GlobalTypes.FindMessageByName(d.FullName())
	if err != nil {
		return nil, fmt.Errorf("rpc: idempotency: message %s: %w", d.FullName(), err)
	}
	return t.New().Interface(), nil
}

// PurgeBatch is how many expired idempotency records [PurgeIdempotencyKeys]
// deletes per transaction.
const PurgeBatch = 1000

// JobKindPurgeIdempotencyKeys is the kind of the per-organization jobs of
// the idempotency purge clock job; [PurgeIdempotencyKeys] runs them.
const JobKindPurgeIdempotencyKeys = "idempotency.purge"

// PurgeArgs are the arguments of a [JobKindPurgeIdempotencyKeys] job.
type PurgeArgs struct {
	// Before is the clock job's instant: records expired then are deleted.
	Before time.Time `json:"before"`
}

// DispatchIdempotencyPurges runs the idempotency purge clock job: it
// enqueues a [JobKindPurgeIdempotencyKeys] job for every organization
// with records expired at now, and returns how many.
func DispatchIdempotencyPurges(ctx context.Context, tx port.Transactor, now time.Time) (int, error) {
	args, err := json.Marshal(PurgeArgs{Before: now.UTC()})
	if err != nil {
		return 0, fmt.Errorf("encode purge args: %w", err)
	}
	var n int
	err = tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		orgs, err := r.IdempotencyKeys.Organizations(ctx, now.UTC())
		if err != nil {
			return err
		}
		jobs := make([]port.Job, len(orgs))
		for i, org := range orgs {
			jobs[i] = port.Job{
				Kind: JobKindPurgeIdempotencyKeys, Queue: port.QueueDefault, OrganizationID: org,
				Args: args, UniqueKey: JobKindPurgeIdempotencyKeys,
			}
		}
		n = len(jobs)
		return r.Jobs.Enqueue(ctx, jobs...)
	})
	if err != nil {
		return 0, fmt.Errorf("dispatch idempotency purges: %w", err)
	}
	return n, nil
}

// PurgeIdempotencyKeys deletes the idempotency records of org expired at
// args.Before, [PurgeBatch] per transaction, and returns how many.
func PurgeIdempotencyKeys(ctx context.Context, tx port.Transactor, org core.OrganizationID, args PurgeArgs) (int, error) {
	total := 0
	for {
		var n int
		err := tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			var err error
			n, err = r.IdempotencyKeys.Purge(ctx, org, args.Before, PurgeBatch)
			return err
		})
		if err != nil {
			return total, fmt.Errorf("purge idempotency keys: %w", err)
		}
		total += n
		if n < PurgeBatch {
			return total, nil
		}
	}
}

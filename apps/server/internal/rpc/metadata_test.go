package rpc_test

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/period"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	creditnotev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/creditnote/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/creditnote/v1/creditnotev1connect"
	walletv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/wallet/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/wallet/v1/walletv1connect"
)

type mdItems = []*commonv1.MetadataItem

// metadataOps are the four metadata RPCs of one resource, bound to the
// resource's ID.
type metadataOps struct {
	replace func(ctx context.Context, id string, md mdItems) (mdItems, error)
	merge   func(ctx context.Context, id string, md mdItems) (mdItems, error)
	del     func(ctx context.Context, id string) (mdItems, error)
	delKey  func(ctx context.Context, id, key string) (mdItems, error)
}

// metadataResource is a resource with metadata endpoints.
type metadataResource struct {
	name     string
	ops      metadataOps
	newID    func(t *testing.T) string // a fresh resource without metadata
	unknown  string                    // an ID that does not exist
	notFound string                    // the reason of the unknown resource
	path     string                    // the REST collection
}

func mdList(kv ...string) mdItems {
	out := make(mdItems, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		out = append(out, commonv1.MetadataItem_builder{Key: new(kv[i]), Value: new(kv[i+1])}.Build())
	}
	return out
}

func mdPairs(md mdItems) []string {
	out := []string{}
	for _, it := range md {
		out = append(out, it.GetKey(), it.GetValue())
	}
	return out
}

func metadataResources(t *testing.T, s *rpctest.Server) []metadataResource {
	t.Helper()
	plans := catalogv1connect.NewPlanServiceClient(s.Connect(s.APIKey))
	cns := creditnotev1connect.NewCreditNoteServiceClient(s.Connect(s.APIKey))
	wallets := walletv1connect.NewWalletServiceClient(s.Connect(s.APIKey))
	cust := s.Fixtures.Customer()
	inv, _ := s.Fixtures.CreditableInvoice(cust, []int64{10, 10}, nil, []func(*invoice.Invoice){func(inv *invoice.Invoice) { inv.TotalPaidAmount = 5 }})
	return []metadataResource{
		{
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:9
			name: "plan", path: "/v1/plans/", unknown: rpc.CodePrefix + "invalid_code", notFound: "plan_not_found",
			newID: func(t *testing.T) string { return rpc.CodePrefix + s.Fixtures.Plan(period.Monthly, 1000).Code },
			ops: metadataOps{
				replace: func(ctx context.Context, id string, md mdItems) (mdItems, error) {
					r, err := plans.ReplacePlanMetadata(ctx, catalogv1.ReplacePlanMetadataRequest_builder{PlanId: new(id), Metadata: md}.Build())
					return r.GetMetadata(), err
				},
				merge: func(ctx context.Context, id string, md mdItems) (mdItems, error) {
					r, err := plans.MergePlanMetadata(ctx, catalogv1.MergePlanMetadataRequest_builder{PlanId: new(id), Metadata: md}.Build())
					return r.GetMetadata(), err
				},
				del: func(ctx context.Context, id string) (mdItems, error) {
					r, err := plans.DeletePlanMetadata(ctx, catalogv1.DeletePlanMetadataRequest_builder{PlanId: new(id)}.Build())
					return r.GetMetadata(), err
				},
				delKey: func(ctx context.Context, id, key string) (mdItems, error) {
					r, err := plans.DeletePlanMetadataKey(ctx, catalogv1.DeletePlanMetadataKeyRequest_builder{PlanId: new(id), Key: new(key)}.Build())
					return r.GetMetadata(), err
				},
			},
		},
		{
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:10
			name: "credit note", path: "/v1/credit_notes/", unknown: core.NewID[core.CreditNoteID](s.Fixtures.IDs).String(), notFound: "credit_note_not_found",
			newID: func(t *testing.T) string { return s.Fixtures.StoredCreditNote(inv, 1).ID.String() },
			ops: metadataOps{
				replace: func(ctx context.Context, id string, md mdItems) (mdItems, error) {
					r, err := cns.ReplaceCreditNoteMetadata(ctx, creditnotev1.ReplaceCreditNoteMetadataRequest_builder{CreditNoteId: new(id), Metadata: md}.Build())
					return r.GetMetadata(), err
				},
				merge: func(ctx context.Context, id string, md mdItems) (mdItems, error) {
					r, err := cns.MergeCreditNoteMetadata(ctx, creditnotev1.MergeCreditNoteMetadataRequest_builder{CreditNoteId: new(id), Metadata: md}.Build())
					return r.GetMetadata(), err
				},
				del: func(ctx context.Context, id string) (mdItems, error) {
					r, err := cns.DeleteCreditNoteMetadata(ctx, creditnotev1.DeleteCreditNoteMetadataRequest_builder{CreditNoteId: new(id)}.Build())
					return r.GetMetadata(), err
				},
				delKey: func(ctx context.Context, id, key string) (mdItems, error) {
					r, err := cns.DeleteCreditNoteMetadataKey(ctx, creditnotev1.DeleteCreditNoteMetadataKeyRequest_builder{CreditNoteId: new(id), Key: new(key)}.Build())
					return r.GetMetadata(), err
				},
			},
		},
		{
			// source: spec/requests/api/v1/wallets/metadata_controller_spec.rb:11
			name: "wallet", path: "/v1/wallets/", unknown: core.NewID[core.WalletID](s.Fixtures.IDs).String(), notFound: "wallet_not_found",
			newID: func(t *testing.T) string { return s.Fixtures.Wallet(s.Fixtures.Customer(), "100").ID.String() },
			ops: metadataOps{
				replace: func(ctx context.Context, id string, md mdItems) (mdItems, error) {
					r, err := wallets.ReplaceWalletMetadata(ctx, walletv1.ReplaceWalletMetadataRequest_builder{WalletId: new(id), Metadata: md}.Build())
					return r.GetMetadata(), err
				},
				merge: func(ctx context.Context, id string, md mdItems) (mdItems, error) {
					r, err := wallets.MergeWalletMetadata(ctx, walletv1.MergeWalletMetadataRequest_builder{WalletId: new(id), Metadata: md}.Build())
					return r.GetMetadata(), err
				},
				del: func(ctx context.Context, id string) (mdItems, error) {
					r, err := wallets.DeleteWalletMetadata(ctx, walletv1.DeleteWalletMetadataRequest_builder{WalletId: new(id)}.Build())
					return r.GetMetadata(), err
				},
				delKey: func(ctx context.Context, id, key string) (mdItems, error) {
					r, err := wallets.DeleteWalletMetadataKey(ctx, walletv1.DeleteWalletMetadataKeyRequest_builder{WalletId: new(id), Key: new(key)}.Build())
					return r.GetMetadata(), err
				},
			},
		},
	}
}

// TestMetadataEndpoints ports the metadata controller specs of plans,
// credit notes and wallets, which share their cases
// (spec/support/shared_examples/wallet_metadata_actions.rb).
func TestMetadataEndpoints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		existing []string // the metadata set first, nil for none, empty for an empty item
		call     func(ctx context.Context, ops metadataOps, id string) (mdItems, error)
		want     []string
		notFound string // the NotFound reason expected instead
	}{
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:18
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:26
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:36
			name: "replace without metadata",
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) {
				return o.replace(ctx, id, mdList("foo", "bar", "baz", "qux"))
			},
			want: []string{"foo", "bar", "baz", "qux"},
		},
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:28
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:36
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:46
			name: "replace existing metadata", existing: []string{"old", "value", "foo", "old"},
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) {
				return o.replace(ctx, id, mdList("foo", "bar", "baz", "qux"))
			},
			want: []string{"foo", "bar", "baz", "qux"},
		},
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:40
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:48
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:58
			name: "replace with nothing", existing: []string{"old", "value"},
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) { return o.replace(ctx, id, nil) },
			want: []string{},
		},
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:54
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:62
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:72
			name: "replace nothing with nothing",
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) { return o.replace(ctx, id, nil) },
			want: []string{},
		},
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:94
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:104
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:114
			name: "merge without metadata",
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) {
				return o.merge(ctx, id, mdList("foo", "bar", "baz", "qux"))
			},
			want: []string{"foo", "bar", "baz", "qux"},
		},
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:104
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:114
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:124
			name: "merge into existing metadata", existing: []string{"old", "value", "foo", "old"},
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) {
				return o.merge(ctx, id, mdList("foo", "bar", "baz", "qux"))
			},
			want: []string{"old", "value", "foo", "bar", "baz", "qux"},
		},
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:116
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:153
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:126
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:136
			name: "merge nothing", existing: []string{"old", "value"},
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) { return o.merge(ctx, id, nil) },
			want: []string{"old", "value"},
		},
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:130
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:142
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:140
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:150
			name: "merge nothing without metadata",
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) { return o.merge(ctx, id, nil) },
			want: []string{},
		},
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:179
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:181
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:191
			name: "delete metadata", existing: []string{"foo", "bar"},
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) { return o.del(ctx, id) },
			want: []string{},
		},
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:191
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:193
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:203
			name: "delete without metadata",
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) { return o.del(ctx, id) },
			want: []string{},
		},
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:216
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:221
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:231
			name:     "delete key without metadata",
			call:     func(ctx context.Context, o metadataOps, id string) (mdItems, error) { return o.delKey(ctx, id, "foo") },
			notFound: "metadata_not_found",
		},
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:223
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:228
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:238
			name: "delete key", existing: []string{"foo", "bar", "baz", "qux"},
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) { return o.delKey(ctx, id, "foo") },
			want: []string{"baz", "qux"},
		},
		{
			// source: spec/support/shared_examples/wallet_metadata_actions.rb:235
			// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:240
			// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:250
			name: "delete missing key", existing: []string{"baz", "qux"},
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) { return o.delKey(ctx, id, "foo") },
			want: []string{"baz", "qux"},
		},
		{
			// Emptied metadata is still a metadata item: the key endpoints
			// answer not found only without one
			// (app/controllers/api/v1/plans/metadata_controller.rb:38,
			// app/controllers/api/v1/credit_notes/metadata_controller.rb:38,
			// app/controllers/concerns/wallet_metadata_actions.rb:38).
			name: "delete key of emptied metadata", existing: []string{},
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) { return o.delKey(ctx, id, "foo") },
			want: []string{},
		},
		{
			name: "delete the last key twice", existing: []string{"foo", "bar"},
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) {
				if _, err := o.delKey(ctx, id, "foo"); err != nil {
					return nil, err
				}
				return o.delKey(ctx, id, "foo")
			},
			want: []string{},
		},
		{
			name: "delete key after deleting metadata", existing: []string{"foo", "bar"},
			call: func(ctx context.Context, o metadataOps, id string) (mdItems, error) {
				if _, err := o.del(ctx, id); err != nil {
					return nil, err
				}
				return o.delKey(ctx, id, "foo")
			},
			notFound: "metadata_not_found",
		},
	}
	s := rpctest.New(t)
	for _, res := range metadataResources(t, s) {
		for _, tt := range tests {
			t.Run(res.name+"/"+tt.name, func(t *testing.T) {
				id := res.newID(t)
				if tt.existing != nil {
					if _, err := res.ops.replace(t.Context(), id, mdList(tt.existing...)); err != nil {
						t.Fatalf("set metadata: %v", err)
					}
				}
				got, err := tt.call(t.Context(), res.ops, id)
				if tt.notFound != "" {
					walletWantNotFound(t, err, tt.notFound)
					return
				}
				if err != nil {
					t.Fatalf("call: %v", err)
				}
				if !slices.Equal(mdPairs(got), tt.want) {
					t.Errorf("metadata: got = %v, want = %v", mdPairs(got), tt.want)
				}
			})
		}
		// source: spec/support/shared_examples/wallet_metadata_actions.rb:9, 85, 170, 207
		// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:17, 95, 172, 212
		// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:18, 105, 182, 222
		t.Run(res.name+"/unknown", func(t *testing.T) {
			ctx := t.Context()
			calls := map[string]func() (mdItems, error){
				"replace": func() (mdItems, error) { return res.ops.replace(ctx, res.unknown, mdList("foo", "bar")) },
				"merge":   func() (mdItems, error) { return res.ops.merge(ctx, res.unknown, mdList("foo", "bar")) },
				"delete":  func() (mdItems, error) { return res.ops.del(ctx, res.unknown) },
				"key":     func() (mdItems, error) { return res.ops.delKey(ctx, res.unknown, "foo") },
			}
			for name, call := range calls {
				_, err := call()
				if connect.CodeOf(err) != connect.CodeNotFound || rpc.ErrorReason(err) != res.notFound {
					t.Errorf("%s: got err = %v (reason %q), want = NotFound %s", name, err, rpc.ErrorReason(err), res.notFound)
				}
			}
		})
		// A body without metadata replaces with, or merges, nothing. Lago
		// credit notes fetch the missing parameter as {}
		// (app/controllers/api/v1/credit_notes/metadata_controller.rb:52);
		// lago plans and wallets skip it and answer null
		// (app/services/plans/update_service.rb:128,
		// app/services/wallets/update_service.rb:223). A repeated field has
		// no presence, so Tanvo treats every resource as an empty list: the
		// replace creates an empty item and the merge creates none, and
		// both answer an empty list.
		// source: spec/support/shared_examples/wallet_metadata_actions.rb:66, 142
		// source: spec/requests/api/v1/plans/metadata_controller_spec.rb:74, 152
		// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:84, 162
		t.Run(res.name+"/REST without metadata", func(t *testing.T) {
			for _, method := range []string{http.MethodPost, http.MethodPatch} {
				status, body := s.REST(t, s.APIKey, method, res.path+res.newID(t)+"/metadata", `{}`)
				if status != http.StatusOK {
					t.Fatalf("%s = %d %s, want = 200", method, status, body)
				}
				if got := decode[struct {
					Metadata []struct{ Key, Value string } `json:"metadata"`
				}](t, body).Metadata; len(got) != 0 {
					t.Errorf("%s: got = %v, want = no metadata", method, got)
				}
			}
		})
		t.Run(res.name+"/REST", func(t *testing.T) {
			path := res.path + res.newID(t) + "/metadata"
			for _, step := range []struct {
				method, path string
				body         any
				want         []string
			}{
				{http.MethodPost, path, `{"metadata":[{"key":"foo","value":"bar"}]}`, []string{"foo", "bar"}},
				{http.MethodPatch, path, `{"metadata":[{"key":"baz","value":"qux"}]}`, []string{"foo", "bar", "baz", "qux"}},
				{http.MethodDelete, path + "/foo", nil, []string{"baz", "qux"}},
				{http.MethodDelete, path, nil, []string{}},
			} {
				status, body := s.REST(t, s.APIKey, step.method, step.path, step.body)
				if status != http.StatusOK {
					t.Fatalf("%s %s = %d %s, want = 200", step.method, step.path, status, body)
				}
				got := []string{}
				for _, it := range decode[struct {
					Metadata []struct{ Key, Value string } `json:"metadata"`
				}](t, body).Metadata {
					got = append(got, it.Key, it.Value)
				}
				if !slices.Equal(got, step.want) {
					t.Errorf("%s %s: got = %v, want = %v", step.method, step.path, got, step.want)
				}
			}
		})
	}
}

// TestMetadataRefusals checks the objects the endpoints do not find and
// the limits of item metadata.
func TestMetadataRefusals(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	ctx := t.Context()
	res := metadataResources(t, s)
	t.Run("draft credit note", func(t *testing.T) {
		// source: spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:27
		cust := s.Fixtures.Customer()
		inv, _ := s.Fixtures.CreditableInvoice(cust, []int64{10}, nil, nil)
		draft := s.Fixtures.StoredCreditNote(inv, 1, func(cn *creditnote.CreditNote) { cn.Status = creditnote.StatusDraft })
		_, err := res[1].ops.replace(ctx, draft.ID.String(), mdList("foo", "bar"))
		walletWantNotFound(t, err, "credit_note_not_found")
	})
	t.Run("override plan", func(t *testing.T) {
		// The endpoints find parent plans only
		// (app/controllers/api/v1/plans/base_controller.rb:14).
		parent := s.Fixtures.Plan(period.Monthly, 1000)
		child := s.Fixtures.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.ParentID = parent.ID })
		_, err := res[0].ops.merge(ctx, child.ID.String(), mdList("foo", "bar"))
		walletWantNotFound(t, err, "plan_not_found")
	})
	for _, r := range res {
		t.Run(r.name+" limits", func(t *testing.T) {
			id := r.newID(t)
			// source: spec/models/item_metadata_spec.rb:31
			long := make([]byte, 101)
			for i := range long {
				long[i] = 'k'
			}
			_, err := r.ops.replace(ctx, id, mdList(string(long), "v"))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("long key: got err = %v, want = InvalidArgument", err)
			}
			// source: spec/models/item_metadata_spec.rb:58
			many := make([]string, 0, 100)
			for i := range 50 {
				many = append(many, "k"+string(rune('A'+i/26))+string(rune('a'+i%26)), "v")
			}
			if _, err := r.ops.replace(ctx, id, mdList(many...)); err != nil {
				t.Fatalf("50 keys: %v", err)
			}
			_, err = r.ops.merge(ctx, id, mdList("one", "more"))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("51 keys: got err = %v, want = InvalidArgument", err)
			}
		})
	}
}

// TestWalletMetadataFields checks the wallet's metadata on create, get
// and update (lago WalletActions#create_params and #update_params permit
// metadata; V1::WalletSerializer renders it).
func TestWalletMetadataFields(t *testing.T) {
	t.Parallel()
	f := newWalletFixture(t)
	in := f.walletInput()
	in.SetMetadata(mdList("foo", "bar"))
	// source: spec/services/wallets/create_service_spec.rb:950
	w, err := f.create(t, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := mdPairs(w.GetMetadata()); !slices.Equal(got, []string{"foo", "bar"}) {
		t.Errorf("created: got = %v, want = [foo bar]", got)
	}
	// Update replaces the metadata (partial_metadata false).
	resp, err := f.c.UpdateWallet(t.Context(), walletv1.UpdateWalletRequest_builder{
		Wallet:     walletv1.Wallet_builder{Id: new(w.GetId()), Metadata: mdList("baz", "qux")}.Build(),
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"metadata"}},
	}.Build())
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := mdPairs(resp.GetWallet().GetMetadata()); !slices.Equal(got, []string{"baz", "qux"}) {
		t.Errorf("updated: got = %v, want = [baz qux]", got)
	}
	got, err := f.c.GetWallet(t.Context(), walletv1.GetWalletRequest_builder{WalletId: new(w.GetId())}.Build())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if md := mdPairs(got.GetWallet().GetMetadata()); !slices.Equal(md, []string{"baz", "qux"}) {
		t.Errorf("get: got = %v, want = [baz qux]", md)
	}
	// An update mask with metadata and no items clears it.
	resp, err = f.c.UpdateWallet(t.Context(), walletv1.UpdateWalletRequest_builder{
		Wallet:     walletv1.Wallet_builder{Id: new(w.GetId())}.Build(),
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"metadata"}},
	}.Build())
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n := len(resp.GetWallet().GetMetadata()); n != 0 {
		t.Errorf("cleared: got = %d items, want = 0", n)
	}
}

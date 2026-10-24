package x402

import (
	"context"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// The Solana reader's paging and windows.
//
// source: app/services/x402/chain/svm_reader.rb:6-12
const (
	svmPageSize       = 1_000
	svmMaxPages       = 50
	svmMaxWindowPages = 5
	svmSinceMargin    = 10 * time.Minute
	svmLandingWindow  = 10 * time.Minute
	svmCommitment     = "finalized"
)

type svmOutcome int

const (
	svmAbsent svmOutcome = iota + 1
	svmSettled
	svmFailedOnChain
)

// svmReader reads whether the buyer's signed transaction landed.
//
// source: app/services/x402/chain/svm_reader.rb
type svmReader struct {
	chain   *ChainReaders
	network x402.Network
	payment x402.Object
	since   time.Time

	client     *rpcClient
	stored     *x402.SolanaPayment
	expiry     *svmExpiry
	outcome    svmOutcome
	identifier string
	expired    *bool
}

type svmExpiry struct {
	slot  int64
	valid bool
}

type svmSignature struct {
	signature string
	blockTime *int64
}

// Settled reports whether the transaction landed and succeeded.
func (r *svmReader) Settled(ctx context.Context) (bool, error) {
	o, err := r.read(ctx)
	return o == svmSettled, err
}

// Identifier is the landed transaction's id, its first signature.
func (r *svmReader) Identifier(ctx context.Context) (string, error) {
	o, err := r.read(ctx)
	if err != nil || o != svmSettled {
		return "", err
	}
	return r.identifier, nil
}

// Final reports whether the transaction landed, or can no longer: its
// blockhash expired at a slot dated past the expiry proof.
//
// source: app/services/x402/chain/svm_reader.rb:29-31,126-144
func (r *svmReader) Final(ctx context.Context) (bool, error) {
	o, err := r.read(ctx)
	if err != nil {
		return false, err
	}
	if o != svmAbsent {
		return true, nil
	}
	if r.expired != nil {
		return *r.expired, nil
	}
	expired := false
	if !r.expiry.valid {
		v, err := r.call(ctx, "getBlockTime", r.expiry.slot)
		if err != nil {
			return false, err
		}
		t, ok := jsonInteger(v)
		if v != nil && !ok {
			return false, inconclusive("getBlockTime answered something other than a time")
		}
		expired = v != nil && t >= r.since.Unix()+int64(x402.SVMExpiryProof/time.Second)
	}
	r.expired = &expired
	return expired, nil
}

// source: app/services/x402/chain/svm_reader.rb:42-62
func (r *svmReader) read(ctx context.Context) (svmOutcome, error) {
	if r.outcome != 0 {
		return r.outcome, nil
	}
	if r.since.IsZero() {
		return 0, errNoSince
	}
	if r.stored == nil {
		p, err := x402.ReadSolanaPayment(r.payment)
		if err != nil {
			return 0, err
		}
		r.stored = p
	}
	if err := r.verifyCluster(ctx); err != nil {
		return 0, err
	}
	if err := r.readExpiry(ctx); err != nil {
		return 0, err
	}
	match, err := r.findLandedTransaction(ctx)
	if err != nil {
		return 0, err
	}
	o := svmAbsent
	if match != nil {
		meta, ok := match.transaction["meta"].(map[string]any)
		errValue, has := meta["err"]
		if !ok || !has {
			return 0, inconclusive("getTransaction returned no status for %s", match.signature)
		}
		o = svmFailedOnChain
		if errValue == nil {
			r.identifier = match.signature
			o = svmSettled
		}
	}
	r.outcome = o
	return o, nil
}

type svmMatch struct {
	signature   string
	transaction x402.Object
}

// findLandedTransaction pages through the buyer's signatures, newest
// first, for the transaction carrying the buyer's signature.
//
// source: app/services/x402/chain/svm_reader.rb:64-87
func (r *svmReader) findLandedTransaction(ctx context.Context) (*svmMatch, error) {
	since := r.since.Unix()
	landing := since + int64(svmLandingWindow/time.Second)
	floor := since - int64(svmSinceMargin/time.Second)
	before := ""
	windowPages := 0
	for range svmMaxPages {
		page, err := r.historyPage(ctx, before)
		if err != nil {
			return nil, err
		}
		candidates := make([]svmSignature, 0, len(page))
		inWindow := false
		for _, e := range page {
			if e.blockTime != nil && *e.blockTime > landing {
				continue
			}
			candidates = append(candidates, e)
			if e.blockTime == nil || *e.blockTime >= floor {
				inWindow = true
			}
		}
		if inWindow {
			windowPages++
		}
		if windowPages > svmMaxWindowPages {
			return nil, inconclusive("the buyer's history around the payment exceeds %d pages", svmMaxWindowPages)
		}
		for _, e := range candidates {
			if e.blockTime != nil && *e.blockTime < floor {
				return nil, nil
			}
			m, err := r.matchCandidate(ctx, e.signature)
			if err != nil || m != nil {
				return m, err
			}
		}
		if len(page) < svmPageSize {
			return nil, nil
		}
		before = page[len(page)-1].signature
	}
	return nil, inconclusive("the buyer's history since the payment exceeds %d pages", svmMaxPages)
}

// source: app/services/x402/chain/svm_reader.rb:93-105
func (r *svmReader) historyPage(ctx context.Context, before string) ([]svmSignature, error) {
	query := map[string]any{"limit": svmPageSize, "commitment": svmCommitment, "minContextSlot": r.expiry.slot}
	if before != "" {
		query["before"] = before
	}
	v, err := r.call(ctx, "getSignaturesForAddress", r.stored.Payer(), query)
	if err != nil {
		return nil, err
	}
	entries, ok := objects(v)
	if !ok {
		return nil, inconclusive("getSignaturesForAddress returned a malformed page")
	}
	page := make([]svmSignature, 0, len(entries))
	for _, e := range entries {
		sig, ok := e["signature"].(string)
		if !ok {
			return nil, inconclusive("getSignaturesForAddress returned a malformed page")
		}
		s := svmSignature{signature: sig}
		if bt := e["blockTime"]; bt != nil {
			t, ok := jsonInteger(bt)
			if !ok {
				return nil, inconclusive("getSignaturesForAddress returned a malformed page")
			}
			s.blockTime = &t
		}
		page = append(page, s)
	}
	return page, nil
}

// source: app/services/x402/chain/svm_reader.rb:107-118
func (r *svmReader) matchCandidate(ctx context.Context, signature string) (*svmMatch, error) {
	v, err := r.call(ctx, "getTransaction", signature,
		map[string]any{"encoding": "json", "commitment": svmCommitment, "maxSupportedTransactionVersion": 1})
	if err != nil {
		return nil, err
	}
	landed, ok := v.(map[string]any)
	if !ok {
		return nil, inconclusive("getTransaction returned no transaction for a listed signature")
	}
	tx, _ := landed["transaction"].(map[string]any)
	signatures, ok := list(tx["signatures"])
	if !ok {
		return nil, inconclusive("getTransaction returned no signatures for %s", signature)
	}
	buyer := x402.Base58Encode(r.stored.BuyerSignature)
	for _, s := range signatures {
		if s == buyer {
			return &svmMatch{signature: signature, transaction: landed}, nil
		}
	}
	return nil, nil
}

// source: app/services/x402/chain/svm_reader.rb:146-149
func (r *svmReader) verifyCluster(ctx context.Context) error {
	v, err := r.call(ctx, "getGenesisHash")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(rubyString(v), r.network.Reference()) {
		return inconclusive("the endpoint for %s serves another cluster", r.network)
	}
	return nil
}

// source: app/services/x402/chain/svm_reader.rb:151-161
func (r *svmReader) readExpiry(ctx context.Context) error {
	if r.expiry != nil {
		return nil
	}
	v, err := r.call(ctx, "isBlockhashValid",
		x402.Base58Encode(r.stored.Transaction.RecentBlockhash), map[string]any{"commitment": svmCommitment})
	if err != nil {
		return err
	}
	answer, _ := v.(map[string]any)
	ctxObj, _ := answer["context"].(map[string]any)
	slot, okSlot := jsonInteger(ctxObj["slot"])
	valid, okValid := answer["value"].(bool)
	if !okSlot || !okValid {
		return inconclusive("isBlockhashValid returned no usable answer")
	}
	r.expiry = &svmExpiry{slot: slot, valid: valid}
	return nil
}

func (r *svmReader) call(ctx context.Context, method string, params ...any) (any, error) {
	if r.client == nil {
		c, err := r.chain.rpc(r.network)
		if err != nil {
			return nil, err
		}
		r.client = c
	}
	return r.client.call(ctx, method, params...)
}

package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/internal/cachecodec"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// DefaultDraftTaxTTL is how long a cached draft tax answer is reused
// (lago DraftTaxesCacheService::DEFAULT_TTL).
const DefaultDraftTaxTTL = 24 * time.Hour

// draftTaxCacheVersion is bumped when the key or the stored value changes
// (lago CACHE_KEY_VERSION).
const draftTaxCacheVersion = "1"

// DraftTaxCache keeps Anrok's answers to draft tax requests, keyed on the
// request itself (lago
// app/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service.rb):
// wallet refreshes, current usage, previews and draft invoices ask for the
// same taxes many times while nothing changed. An answer is reused only
// for an identical request: the key covers everything sent but the line
// identifiers, which are put back from the current request on a hit, and
// the integration's last change. Only answers carrying lines are kept.
//
// Answers are encoded into a [port.Cache], in the process or on Redis or
// Valkey: with a shared backend every server replica reuses the answers
// any of them received. A nil *DraftTaxCache caches nothing. It is safe
// for concurrent use.
type DraftTaxCache struct {
	c   port.Cache
	ttl time.Duration
	now func() time.Time
}

// draftTaxEntry is the encoding of a kept answer.
type draftTaxEntry struct {
	Result *integration.TaxResult `json:"result"`
	// Expires is the end of the answer's TTL on the cache's clock, which
	// the backend's own expiry does not follow.
	Expires time.Time `json:"expires"`
}

// NewDraftTaxCache returns a cache keeping answers in c for ttl; a nil c
// or a ttl of zero or less turns it off, reads included (lago
// LAGO_ANROK_DRAFT_TAXES_CACHE_TTL_SECONDS). now is the clock; nil uses
// time.Now.
func NewDraftTaxCache(c port.Cache, ttl time.Duration, now func() time.Time) *DraftTaxCache {
	if now == nil {
		now = time.Now
	}
	return &DraftTaxCache{c: c, ttl: ttl, now: now}
}

// key returns the cache key of t for the integration i, or "" when the
// cache is off.
func (c *DraftTaxCache) key(i *integration.Integration, t *integration.TaxTransaction) string {
	if c == nil || c.c == nil || c.ttl <= 0 {
		return ""
	}
	k := *t
	k.Lines = make([]integration.TaxLine, len(t.Lines))
	for n, l := range t.Lines {
		l.ItemKey, l.ItemID = "", ""
		k.Lines[n] = l
	}
	b, err := json.Marshal(k)
	if err != nil {
		return ""
	}
	// The shape of the entries joins the request in the digest
	// (cachecodec.Fingerprint).
	sum := sha256.Sum256(append(b, cachecodec.Fingerprint[draftTaxEntry]()...))
	return strings.Join([]string{
		"anrok-draft-taxes", draftTaxCacheVersion, i.ID.String(),
		i.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000000Z07:00"), hex.EncodeToString(sum[:]),
	}, "/")
}

// get returns the answer cached under key, with the line identifiers of t.
func (c *DraftTaxCache) get(ctx context.Context, key string, t *integration.TaxTransaction) (*integration.TaxResult, bool) {
	if key == "" {
		return nil, false
	}
	b, ok := c.c.Get(ctx, key)
	if !ok {
		return nil, false
	}
	var e draftTaxEntry
	if err := cachecodec.Unmarshal(b, &e); err != nil || e.Result == nil || !c.now().Before(e.Expires) {
		// An answer past its TTL, or written by another version, is a miss.
		c.c.Delete(ctx, key)
		return nil, false
	}
	// The provider answers lines in the order they were sent
	// (draft_taxes_cache_service.rb:79-89).
	for n := range e.Result.Lines {
		if n < len(t.Lines) {
			e.Result.Lines[n].ItemKey, e.Result.Lines[n].ItemID = t.Lines[n].ItemKey, t.Lines[n].ItemID
		}
	}
	return e.Result, true
}

// put caches res under key when it carries lines
// (draft_taxes_cache_service.rb:72-74).
func (c *DraftTaxCache) put(ctx context.Context, key string, res *integration.TaxResult) {
	if key == "" || res == nil || len(res.Lines) == 0 {
		return
	}
	b, err := cachecodec.Marshal(draftTaxEntry{Result: res, Expires: c.now().Add(c.ttl)})
	if err != nil {
		// Answers always encode; were one not to, the provider is asked
		// again next time.
		return
	}
	c.c.Set(ctx, key, b, c.ttl)
}

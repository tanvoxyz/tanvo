package integrations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// DefaultDraftTaxTTL is how long a cached draft tax answer is reused
// (lago DraftTaxesCacheService::DEFAULT_TTL).
const DefaultDraftTaxTTL = 24 * time.Hour

// defaultDraftTaxEntries bounds the entries of a [DraftTaxCache].
const defaultDraftTaxEntries = 10_000

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
// Unlike lago's Rails cache, it lives in the process: each server keeps
// its own entries. It is safe for concurrent use.
type DraftTaxCache struct {
	ttl time.Duration
	max int
	now func() time.Time

	mu      sync.Mutex
	entries map[string]draftTaxEntry
}

type draftTaxEntry struct {
	result  *integration.TaxResult
	expires time.Time
}

// NewDraftTaxCache returns a cache keeping answers for ttl; a ttl of zero
// or less turns it off, reads included (lago
// LAGO_ANROK_DRAFT_TAXES_CACHE_TTL_SECONDS). now is the clock; nil uses
// time.Now.
func NewDraftTaxCache(ttl time.Duration, now func() time.Time) *DraftTaxCache {
	if now == nil {
		now = time.Now
	}
	return &DraftTaxCache{ttl: ttl, max: defaultDraftTaxEntries, now: now, entries: map[string]draftTaxEntry{}}
}

// key returns the cache key of t for the integration i, or "" when the
// cache is off.
func (c *DraftTaxCache) key(i *integration.Integration, t *integration.TaxTransaction) string {
	if c == nil || c.ttl <= 0 {
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
	sum := sha256.Sum256(b)
	return strings.Join([]string{
		"anrok-draft-taxes", draftTaxCacheVersion, i.ID.String(),
		i.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000000Z07:00"), hex.EncodeToString(sum[:]),
	}, "/")
}

// get returns the answer cached under key, with the line identifiers of t.
func (c *DraftTaxCache) get(key string, t *integration.TaxTransaction) (*integration.TaxResult, bool) {
	if key == "" {
		return nil, false
	}
	c.mu.Lock()
	e, ok := c.entries[key]
	if ok && !c.now().Before(e.expires) {
		delete(c.entries, key)
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return nil, false
	}
	out := &integration.TaxResult{TransactionID: e.result.TransactionID, Lines: make([]integration.TaxLineResult, len(e.result.Lines))}
	for n, l := range e.result.Lines {
		l.Breakdown = append([]integration.TaxBreakdown(nil), l.Breakdown...)
		if n < len(t.Lines) {
			l.ItemKey, l.ItemID = t.Lines[n].ItemKey, t.Lines[n].ItemID
		}
		out.Lines[n] = l
	}
	return out, true
}

// put caches res under key when it carries lines.
func (c *DraftTaxCache) put(key string, res *integration.TaxResult) {
	if key == "" || res == nil || len(res.Lines) == 0 {
		return
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < c.max {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[key] = draftTaxEntry{result: res, expires: now.Add(c.ttl)}
}

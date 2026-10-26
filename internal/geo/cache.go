package geo

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// maxCacheEntries bounds a cache: when it is full it is emptied, which is crude and enough for a cache of place names.
const maxCacheEntries = 10000

// cached is a Resolver that remembers the answers of another one.
type cached struct {
	Resolver
	mu sync.Mutex
	m  map[string]cacheEntry
}

type cacheEntry struct {
	place *Place // nil: the resolver knows no such place
}

// Cached wraps r so that a query asked before is answered from memory: places and "not found" are kept, failures of r
// are not. Queries are compared ignoring case and surrounding space.
func Cached(r Resolver) Resolver { return &cached{Resolver: r, m: map[string]cacheEntry{}} }

func cacheKey(q Query) string {
	return strings.ToLower(strings.Join(strings.Fields(q.Text), " ")) + "\x00" + NormKind(q.Kind) + "\x00" + strings.ToLower(q.CountryCode)
}

func (c *cached) Resolve(ctx context.Context, q Query) (*Place, error) {
	k := cacheKey(q)
	c.mu.Lock()
	e, ok := c.m[k]
	c.mu.Unlock()
	if ok {
		if e.place == nil {
			return nil, ErrNotFound
		}
		p := *e.place
		return &p, nil
	}
	p, err := c.Resolver.Resolve(ctx, q)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	c.mu.Lock()
	if len(c.m) >= maxCacheEntries {
		c.m = map[string]cacheEntry{}
	}
	if err == nil {
		cp := *p
		c.m[k] = cacheEntry{place: &cp}
	} else {
		c.m[k] = cacheEntry{}
	}
	c.mu.Unlock()
	return p, err
}

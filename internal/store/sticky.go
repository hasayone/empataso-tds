package store

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/redis/go-redis/v9"
)

// stickyKey is the Redis key that remembers which stream of a group served an IP.
//
// group is the serving group id and ip the visitor address. It returns
// "st:<group>:<ip>". The value stored under it is an opaque token, so the key
// stays short whatever the stream name is.
func stickyKey(group string, ip netip.Addr) string {
	return "st:" + group + ":" + ip.String()
}

// StickyGet reads the token remembered for the pair (group, ip).
//
// ctx bounds the Redis call. group is the serving group id and ip the visitor.
// It returns the token, or "" when nothing is remembered.
// A Redis failure returns "" with the error: the caller treats it as nothing
// remembered (fail-open).
func (c *Counters) StickyGet(ctx context.Context, group string, ip netip.Addr) (string, error) {
	token, err := c.rdb.Get(ctx, stickyKey(group, ip)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

// StickySet remembers token for the pair (group, ip) for ttl.
//
// ctx bounds the Redis call. token is the opaque value the engine signed for
// the drawn stream. A non-positive ttl is 24h. It returns the Redis error,
// which the caller ignores: a stream that was not saved is drawn again.
func (c *Counters) StickySet(ctx context.Context, group string, ip netip.Addr, token string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return c.rdb.Set(ctx, stickyKey(group, ip), token, ttl).Err()
}

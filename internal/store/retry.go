package store

import (
	"context"
	"net"
	"time"

	"github.com/redis/go-redis/v9"
)

// How long a process waits for Redis or ClickHouse during startup.
// The budget is about a minute. The pause is the gap after a failed dial.
// Each dial fails on its own timeout; the client must not retry inside that dial,
// or one attempt lasts as long as the whole budget and the process never listens.
const (
	ConnectBudget = time.Minute
	ConnectPause  = 2 * time.Second
)

// Retry calls fn until it returns nil or attempts run out.
// pause is the wait after a failed call; a success returns immediately and
// does not wait. attempts below 1 still calls fn once. When every call fails,
// Retry returns the last error.
func Retry(attempts int, pause time.Duration, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := 0; i < attempts; i++ {
		err = fn()
		if err == nil {
			return nil
		}
		if pause > 0 && i+1 < attempts {
			time.Sleep(pause)
		}
	}
	return err
}

// RetryWithin calls fn until it returns nil or budget has passed.
// pause is the wait after a failed call. A success returns immediately.
// When the budget runs out, RetryWithin returns the last error, including
// when that last call itself used up the remaining time.
func RetryWithin(budget, pause time.Duration, fn func() error) error {
	if budget <= 0 {
		return fn()
	}
	deadline := time.Now().Add(budget)
	var err error
	for {
		err = fn()
		if err == nil || !time.Now().Before(deadline) {
			return err
		}
		wait := pause
		if remain := time.Until(deadline); remain < wait {
			wait = remain
		}
		if wait > 0 {
			time.Sleep(wait)
		}
		if !time.Now().Before(deadline) {
			return err
		}
	}
}

// OpenRedisWaiting dials Redis until it answers or the startup wait is over.
// addr, password and db select the server. On success the client is returned.
// On failure the last dial error is returned and no client is left open.
func OpenRedisWaiting(addr, password string, db int) (*redis.Client, error) {
	var c *redis.Client
	err := RetryWithin(ConnectBudget, ConnectPause, func() error {
		var dial error
		c, dial = openRedisStartup(addr, password, db)
		return dial
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// openRedisStartup dials once. MaxRetries is -1 so the startup loop is the
// only retry: the default client repeats a failed dial several times and one
// attempt then outlasts ConnectBudget.
func openRedisStartup(addr, password string, db int) (*redis.Client, error) {
	c := redis.NewClient(&redis.Options{
		Addr:        addr,
		Password:    password,
		DB:          db,
		DialTimeout: 2 * time.Second,
		MaxRetries:  -1,
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, addr)
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// OpenCHWaiting dials ClickHouse until it answers or the startup wait is over.
// addr, database, username and password are passed to OpenCH. On success the
// client is returned. On failure the last dial error is returned and no
// connection is left open.
func OpenCHWaiting(addr, database, username, password string) (*CH, error) {
	var ch *CH
	err := RetryWithin(ConnectBudget, ConnectPause, func() error {
		var dial error
		ch, dial = OpenCH(addr, database, username, password)
		return dial
	})
	if err != nil {
		return nil, err
	}
	return ch, nil
}

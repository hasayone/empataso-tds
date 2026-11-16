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

// redisStartupDial bounds one connect when RedisTimeouts.Dial is zero.
const redisStartupDial = 2 * time.Second

// OpenRedisWaiting dials Redis until it answers or the startup wait is over.
// addr, password and db select the server. t bounds the client's network
// waits for its whole life; a zero RedisTimeouts keeps the go-redis defaults
// and a 2s connect. On success the client is returned. On failure the last
// dial error is returned and no client is left open.
func OpenRedisWaiting(addr, password string, db int, t RedisTimeouts) (*redis.Client, error) {
	var c *redis.Client
	err := RetryWithin(ConnectBudget, ConnectPause, func() error {
		var dial error
		c, dial = openRedisStartup(redisOptions(addr, password, db, t))
		return dial
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// NewRedisClient returns a client for addr without dialling or pinging it:
// go-redis connects on the first command and again after every failure, so the
// client is usable as soon as the server is. addr, password, db and t are as in
// OpenRedisWaiting. The caller closes the client.
func NewRedisClient(addr, password string, db int, t RedisTimeouts) *redis.Client {
	return redis.NewClient(redisOptions(addr, password, db, t))
}

// redisOptions builds the client options for addr, password and db with the
// waits from t. MaxRetries is -1 so the startup loop is the only retry: the
// default client repeats a failed dial several times and one attempt then
// outlasts ConnectBudget. A zero t.Dial means a 2s connect; other zero fields,
// including DialRetries, stay at the go-redis default. It does not fail.
func redisOptions(addr, password string, db int, t RedisTimeouts) *redis.Options {
	dial := t.Dial
	if dial <= 0 {
		dial = redisStartupDial
	}
	return &redis.Options{
		Addr:          addr,
		Password:      password,
		DB:            db,
		DialTimeout:   dial,
		ReadTimeout:   t.Read,
		WriteTimeout:  t.Write,
		PoolTimeout:   t.Pool,
		DialerRetries: t.DialRetries,
		MaxRetries:    -1,
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: dial}).DialContext(ctx, network, addr)
		},
	}
}

// openRedisStartup dials once with opts and pings within 2s.
// On a failed ping the client is closed and the error returned.
func openRedisStartup(opts *redis.Options) (*redis.Client, error) {
	c := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), redisStartupDial)
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
	return OpenCHWaitingCtx(context.Background(), addr, database, username, password)
}

// OpenCHWaitingCtx is OpenCHWaiting that also stops when ctx ends: no new dial
// starts after that and the pause between dials is cut short. A dial already in
// progress finishes on its own timeout. It returns ctx.Err() when ctx ended
// first and the last dial error when the startup wait ran out.
func OpenCHWaitingCtx(ctx context.Context, addr, database, username, password string) (*CH, error) {
	deadline := time.Now().Add(ConnectBudget)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ch, err := OpenCH(addr, database, username, password)
		if err == nil {
			return ch, nil
		}
		wait := min(ConnectPause, time.Until(deadline))
		if wait <= 0 {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		if !time.Now().Before(deadline) {
			return nil, err
		}
	}
}

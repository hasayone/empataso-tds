package logbuf

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"sync/atomic"
	"time"
)

// tokenSeq numbers the tokens of this process for the fallback of newToken.
var tokenSeq atomic.Uint64

// dedupKey is the context key of a batch's idempotency token.
type dedupKey struct{}

// WithDedupToken returns ctx carrying the idempotency token of a batch.
// The store reads it with DedupToken and asks ClickHouse to drop a block it has
// already accepted under the same token, so repeating an insert whose answer
// was lost does not write the rows twice. The token travels in the context and
// not in Inserter, so the interface and every implementation of it stay as they
// are. An empty token returns ctx unchanged.
func WithDedupToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, dedupKey{}, token)
}

// DedupToken returns the token WithDedupToken put on ctx, or "" when there is
// none. An insert without a token is not deduplicated.
func DedupToken(ctx context.Context) string {
	t, _ := ctx.Value(dedupKey{}).(string)
	return t
}

// newToken returns a random token for a new batch: 128 random bits in hex.
// Random and not a hash of the content: the list of columns a batch is written
// with can change between two attempts, so equal batches must not need equal
// bytes. When the system has no entropy source the token is the clock reading
// and a counter of this process, which is still unique for every batch it makes.
func newToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(b[8:], tokenSeq.Add(1))
	}
	return hex.EncodeToString(b[:])
}

// ErrNotConnected is what an Inserter returns while it has no storage
// connection yet. A batch whose attempts all failed with it was never sent, so
// it may be repeated even when the storage cannot deduplicate.
var ErrNotConnected = errors.New("logbuf: storage is not connected")

// errAbandoned is the error of an insert the buffer did not start because Close
// had already cancelled its context. Nothing was sent.
var errAbandoned = errors.New("logbuf: insert abandoned before it was sent")

// BadData marks err as the storage refusing the data itself: a value it cannot
// parse, convert or store, as opposed to a timeout, a lost connection or an
// overloaded server. The same batch fails the same way every time, so the
// buffer counts such failures of a spool segment toward setting it aside, and
// counts no others. It returns nil for a nil err.
func BadData(err error) error {
	if err == nil {
		return nil
	}
	return &badDataError{err}
}

// badDataError is the error BadData returns; it unwraps to the original.
type badDataError struct{ err error }

func (e *badDataError) Error() string { return e.err.Error() }
func (e *badDataError) Unwrap() error { return e.err }

// IsBadData reports whether err, or an error it wraps, came from BadData.
func IsBadData(err error) bool {
	var d *badDataError
	return errors.As(err, &d)
}

// neverSent reports whether a failed insert certainly did not reach the
// storage: there was no connection, the dial failed, or Close cancelled the
// insert before it started. Anything else — a timeout, a reset, a server error
// — may have written the rows.
func neverSent(err error) bool {
	if errors.Is(err, ErrNotConnected) || errors.Is(err, errAbandoned) {
		return true
	}
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

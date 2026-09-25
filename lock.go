// Package corelock è il lock distribuito di GPA: un solo motore, N backend di storage.
//
// Un backend non implementa il locking — implementa tre operazioni su una riga di lease
// (vedi LeaseStore) — mentre il motore (lease.go) tiene per tutti la risoluzione dei default, la
// generazione del token, il loop dei tentativi e la semantica di Release ed Extend. È ciò che
// impedisce a due backend di rispondere diversamente alla stessa domanda: prima Acquire era scritta
// due volte identica, e le tre implementazioni divergevano su scadenza, token e Release senza che
// nulla lo rilevasse.
//
// L'acquisizione si configura per chiamata con le AcquireOption: il default è un tentativo solo,
// non bloccante (il dispatch-dedup su cui si regge lo scheduler batch), mentre chi ha bisogno di
// mutua esclusione su una sezione critica può attendere il lock (WithTries/WithRetryDelay) e
// rinnovarne il TTL mentre lavora (Handle.Extend).
//
//	corelock.Module(&svc.Lock, corelock.WithBackend(mongostore.Module))
package corelock

import (
	"context"
	"errors"
	"time"
)

// ErrNotAcquired is returned by Locker.Acquire when the lock is currently held
// by another owner. It signals contention, not an operational failure: callers
// typically skip their critical section and retry later.
var ErrNotAcquired = errors.New("lock: not acquired")

// ErrLockLost is returned by Handle.Extend when the lock is no longer held by
// this owner (the TTL lapsed and it was stolen, or it was already released), so
// the renewal could not be applied.
var ErrLockLost = errors.New("lock: lost")

// AcquireConfig holds the resolved acquisition parameters. The zero value means
// "backend defaults": a single non-blocking attempt with the backend's default
// TTL — the dispatch-dedup behaviour the batch scheduler relies on.
type AcquireConfig struct {
	// Tries is the number of acquisition attempts. <= 1 (the default) means a
	// single, non-blocking attempt: contention returns ErrNotAcquired immediately.
	Tries int
	// RetryDelay is the wait between attempts when Tries > 1.
	RetryDelay time.Duration
	// Expiry is the lock TTL. 0 means the backend default.
	Expiry time.Duration
}

// AcquireOption configures a single Acquire call.
type AcquireOption func(*AcquireConfig)

// WithTries sets the number of acquisition attempts. n > 1 makes Acquire block,
// retrying on contention (RetryDelay between attempts) until it succeeds, the
// attempts are exhausted, or the context is done.
func WithTries(n int) AcquireOption {
	return func(c *AcquireConfig) { c.Tries = n }
}

// WithRetryDelay sets the wait between acquisition attempts (effective only when
// Tries > 1).
func WithRetryDelay(d time.Duration) AcquireOption {
	return func(c *AcquireConfig) { c.RetryDelay = d }
}

// WithExpiry sets the lock TTL. Use Handle.Extend to renew it for a critical
// section that outlives a single TTL.
func WithExpiry(d time.Duration) AcquireOption {
	return func(c *AcquireConfig) { c.Expiry = d }
}

// WithWait is a convenience for blocking acquisition: it retries every delay for
// up to total, i.e. it sets Tries = total/delay (at least 1) and RetryDelay = delay.
func WithWait(total, delay time.Duration) AcquireOption {
	return func(c *AcquireConfig) {
		tries := 1
		if delay > 0 {
			if n := int(total / delay); n > tries {
				tries = n
			}
		}
		c.Tries = tries
		c.RetryDelay = delay
	}
}

// ResolveAcquireConfig applies opts onto the zero AcquireConfig and returns the
// result. Backends use it to interpret the options with uniform semantics.
func ResolveAcquireConfig(opts ...AcquireOption) AcquireConfig {
	var c AcquireConfig
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// Locker acquires named distributed locks.
type Locker interface {
	// Acquire attempts to take the lock identified by key. On success it returns
	// a Handle that must be released. Without options it makes a single attempt and
	// returns ErrNotAcquired when the lock is already held elsewhere; with
	// WithTries/WithRetryDelay it blocks and retries. It returns a non-nil error on
	// backend failure.
	Acquire(ctx context.Context, key string, opts ...AcquireOption) (Handle, error)
}

// Handle represents a lock that has been acquired.
type Handle interface {
	// Release frees the lock. It is safe to call once per acquired Handle.
	Release(ctx context.Context) error
	// Extend renews the lock's TTL so a long critical section does not let it
	// lapse. It returns ErrLockLost if the lock is no longer held by this owner.
	Extend(ctx context.Context) error
}

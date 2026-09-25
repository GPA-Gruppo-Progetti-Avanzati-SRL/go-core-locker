package corelock

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"
)

// spyStore registra ciò che il motore gli chiede. I test del motore riguardano le decisioni che il
// motore prende — chiave, token, TTL, quando riprovare — non lo storage, che ha la conformance.
type spyStore struct {
	keys     []string
	tokens   []string
	ttls     []time.Duration
	err      error // se non nil, TryAcquire lo ritorna
	attempts int

	relKey, relToken string
}

func (s *spyStore) TryAcquire(_ context.Context, key, token string, ttl time.Duration) error {
	s.attempts++
	s.keys = append(s.keys, key)
	s.tokens = append(s.tokens, token)
	s.ttls = append(s.ttls, ttl)
	return s.err
}
func (s *spyStore) Renew(context.Context, string, string, time.Duration) error { return nil }
func (s *spyStore) Release(_ context.Context, key, token string) error {
	s.relKey, s.relToken = key, token
	return nil
}

func TestKeyPrefix_IsolaLeApplicazioni(t *testing.T) {
	s := &spyStore{}
	l := NewLocker(s, &Config{KeyPrefix: "myapp"})

	if _, err := l.Acquire(context.Background(), "import"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got := s.keys[0]; got != "myapp:import" {
		t.Errorf("chiave = %q, attesa myapp:import — senza prefisso due app sullo stesso backend collidono", got)
	}
}

func TestKeyPrefix_AssenteLasciaLaChiaveIntatta(t *testing.T) {
	s := &spyStore{}
	l := NewLocker(s, &Config{})

	if _, err := l.Acquire(context.Background(), "import"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got := s.keys[0]; got != "import" {
		t.Errorf("chiave = %q, attesa import", got)
	}
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestToken_CryptoRandomEDiverso(t *testing.T) {
	s := &spyStore{}
	l := NewLocker(s, nil)
	ctx := context.Background()

	for range 3 {
		if _, err := l.Acquire(ctx, "k"); err != nil {
			t.Fatalf("Acquire: %v", err)
		}
	}

	for _, tok := range s.tokens {
		if !hex32.MatchString(tok) {
			t.Errorf("token = %q: attesi 16 byte in esadecimale", tok)
		}
	}
	if s.tokens[0] == s.tokens[1] || s.tokens[1] == s.tokens[2] {
		t.Error("due acquisizioni hanno lo stesso token: il token distingue i proprietari")
	}
}

func TestRelease_UsaChiavePrefissataEToken(t *testing.T) {
	s := &spyStore{}
	l := NewLocker(s, &Config{KeyPrefix: "app"})

	h, err := l.Acquire(context.Background(), "k")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := h.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if s.relKey != "app:k" || s.relToken != s.tokens[0] {
		t.Errorf("Release(%q, %q), attesi (app:k, %q)", s.relKey, s.relToken, s.tokens[0])
	}
}

// Un guasto del backend non è contesa: riprovarlo significa insistere su un database che non
// risponde, mentre la contesa è l'unica condizione che un altro tentativo può risolvere.
func TestGuastoDelBackendNonSiRiprova(t *testing.T) {
	boom := errors.New("backend irraggiungibile")
	s := &spyStore{err: boom}
	l := NewLocker(s, nil)

	_, err := l.Acquire(context.Background(), "k", WithTries(5), WithRetryDelay(time.Millisecond))
	if !errors.Is(err, boom) {
		t.Fatalf("= %v, atteso l'errore del backend", err)
	}
	if s.attempts != 1 {
		t.Errorf("tentativi = %d, atteso 1: solo la contesa si riprova", s.attempts)
	}
}

func TestContesaSiRiprovaFinoAiTentativi(t *testing.T) {
	s := &spyStore{err: ErrNotAcquired}
	l := NewLocker(s, nil)

	_, err := l.Acquire(context.Background(), "k", WithTries(4), WithRetryDelay(time.Millisecond))
	if !errors.Is(err, ErrNotAcquired) {
		t.Fatalf("= %v, atteso ErrNotAcquired", err)
	}
	if s.attempts != 4 {
		t.Errorf("tentativi = %d, attesi 4", s.attempts)
	}
}

func TestExpiryPerChiamataVinceSulDefault(t *testing.T) {
	s := &spyStore{}
	l := NewLocker(s, &Config{TTL: time.Minute})
	ctx := context.Background()

	if _, err := l.Acquire(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if s.ttls[0] != time.Minute {
		t.Errorf("ttl = %v, atteso il default di config", s.ttls[0])
	}

	if _, err := l.Acquire(ctx, "k", WithExpiry(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if s.ttls[1] != 5*time.Second {
		t.Errorf("ttl = %v, atteso quello della chiamata", s.ttls[1])
	}
}

func TestConfigWithDefaults(t *testing.T) {
	c := (&Config{}).WithDefaults()
	if c.TTL != DefaultTTL || c.RetryDelay != DefaultRetryDelay {
		t.Errorf("default non applicati: %+v", c)
	}
	if c.Mongo.Collection != DefaultLeaseTable || c.Sql.Table != DefaultLeaseTable {
		t.Errorf("default dei backend non applicati: %+v", c)
	}
	if c.KeyPrefix != "" {
		t.Error("il prefisso non ha un default: inventarne uno cambierebbe le chiavi di chi non lo usa")
	}

	before := *c
	if after := c.WithDefaults(); *after != before {
		t.Error("WithDefaults non è idempotente")
	}
}

func TestNewLocker_AccettaConfigNulla(t *testing.T) {
	s := &spyStore{}
	l := NewLocker(s, nil)

	if _, err := l.Acquire(context.Background(), "k"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if s.ttls[0] != DefaultTTL {
		t.Errorf("ttl = %v, atteso DefaultTTL", s.ttls[0])
	}
}

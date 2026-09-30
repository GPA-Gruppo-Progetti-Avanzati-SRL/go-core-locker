package corelock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	core "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-app"
)

// liberr costruisce gli errori del package con l'ambito della libreria (vedi core.Errors).
var liberr = core.Errors{Ambit: Ambit}

// Default del motore. Sono del motore e non dei backend perché lo stesso YAML deve comportarsi
// allo stesso modo su Mongo, SQL e Redis: prima ogni backend aveva i suoi, e il retry delay di uno
// era un intervallo casuale mentre negli altri era una costante.
const (
	// DefaultTTL è la durata del lease quando la chiamata non ne chiede una.
	//
	// Il lock è un'ottimizzazione di dispatch-dedup — la correttezza del batch sta nel claiming sul
	// database — quindi un TTL modesto con un tentativo solo è il comportamento inteso.
	DefaultTTL = 30 * time.Second
	// DefaultRetryDelay è l'attesa fra due tentativi quando ne è stato chiesto più di uno.
	DefaultRetryDelay = 100 * time.Millisecond
	// tokenBytes è la dimensione del token di proprietà. 16 byte crypto-random: il token distingue
	// i proprietari e nessuno deve poterlo indovinare per liberare il lock di un altro.
	tokenBytes = 16
)

// leaseLocker è il motore: implementa Locker sopra un LeaseStore.
//
// È l'unica implementazione di Locker della libreria. I backend ne sono lo storage, e non possono
// divergere sulla semantica perché non ne scrivono nessuna parte.
type leaseLocker struct {
	store      LeaseStore
	prefix     string
	ttl        time.Duration
	retryDelay time.Duration
}

// NewLocker costruisce il Locker sopra lo store indicato.
//
// È esportata perché la usano i Module dei backend e i test di conformità; un'applicazione passa
// invece dal wiring (corelock.Module + WithBackend).
func NewLocker(store LeaseStore, cfg *Config) Locker {
	c := cfg.withDefaults()
	return &leaseLocker{
		store:      store,
		prefix:     c.KeyPrefix,
		ttl:        c.TTL,
		retryDelay: c.RetryDelay,
	}
}

// key applica il prefisso configurato.
//
// Senza prefisso due applicazioni che condividono lo stesso Redis o lo stesso database si
// contendono il lock sui nomi dei propri job, che spesso coincidono — "import", "purge" — senza che
// nulla lo renda visibile: ognuna vede semplicemente dei tick che non partono.
func (l *leaseLocker) key(k string) string {
	if l.prefix == "" {
		return k
	}
	return l.prefix + ":" + k
}

// Acquire tenta di prendere il lock, riprovando se le opzioni lo chiedono.
//
// È il codice che prima stava scritto due volte identico, in go-core-mongo/locker e
// go-core-sql/locker, e una terza volta delegato a redsync con parametri diversi.
func (l *leaseLocker) Acquire(ctx context.Context, name string, opts ...AcquireOption) (Handle, error) {
	cfg := ResolveAcquireConfig(opts...)

	ttl := l.ttl
	if cfg.Expiry > 0 {
		ttl = cfg.Expiry
	}
	tries := max(cfg.Tries, 1)
	delay := cfg.RetryDelay
	if delay <= 0 {
		delay = l.retryDelay
	}

	key := l.key(name)
	token, err := newToken()
	if err != nil {
		return nil, err
	}

	for attempt := 0; ; attempt++ {
		err := l.store.TryAcquire(ctx, key, token, ttl)
		if err == nil {
			return &leaseHandle{store: l.store, key: key, token: token, ttl: ttl}, nil
		}
		if !errors.Is(err, ErrNotAcquired) {
			return nil, err
		}
		if attempt+1 >= tries {
			return nil, ErrNotAcquired
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// leaseHandle è il lock acquisito: porta il token, che è ciò che distingue questo proprietario da
// chi prenderà la chiave dopo di lui.
type leaseHandle struct {
	store LeaseStore
	key   string
	token string
	ttl   time.Duration
}

// Extend rinnova il lease. Ritorna ErrLockLost se nel frattempo la chiave è passata ad altri: è il
// segnale che la sezione critica sta girando senza protezione, e va trattato come tale.
func (h *leaseHandle) Extend(ctx context.Context) error {
	return h.store.Renew(ctx, h.key, h.token, h.ttl)
}

// Release libera il lock. Non segnala il lock già perso, ed è deliberato: l'unico sito di chiamata
// è un defer, che di fronte a quell'errore non avrebbe nulla da scegliere.
func (h *leaseHandle) Release(ctx context.Context) error {
	return h.store.Release(ctx, h.key, h.token)
}

// newToken genera il token di proprietà.
//
// crypto/rand e non un identificatore derivato dal tempo: il token serve a impedire che un
// proprietario liberi o rinnovi il lock di un altro, quindi un valore indovinabile non lo protegge
// da nulla.
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", liberr.Tech(CodeToken).WithCause(err)
	}
	return hex.EncodeToString(b), nil
}

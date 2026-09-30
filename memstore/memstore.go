// Package memstore tiene i lease nella memoria del processo.
//
// Serve a due cose, ed entrambe contano:
//
//   - è il riferimento contro cui il motore si prova, quindi la suite di conformità gira sempre,
//     anche dove non c'è un database o un Redis;
//   - è un Locker legittimo per un'applicazione a replica singola, che altrimenti dovrebbe
//     deployare un backend condiviso per un lock che non ha nessuno con cui condividerlo.
//
// Non è distribuito, e dirlo è il punto: un secondo processo non vede questi lease.
package memstore

import (
	"context"
	"sync"
	"time"

	core "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-app"
	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
)

// liberr costruisce gli errori del package con l'ambito della libreria (vedi core.Errors).
var liberr = core.Errors{Ambit: corelock.Ambit}

// Module registra il backend in-process. Si passa a corelock.WithBackend per riferimento diretto:
//
//	corelock.Module(&svc.Lock, corelock.WithBackend(memstore.Module))
func Module(modes ...string) {
	core.ProvideAs[corelock.LeaseStore](New, modes...)
}

type lease struct {
	token     string
	expiresAt time.Time
}

// Store è il LeaseStore in memoria.
type Store struct {
	mu     sync.Mutex
	leases map[string]lease
	// now è l'orologio, sostituibile dai test per far scadere un lease senza attendere.
	now func() time.Time
}

// New costruisce lo store.
func New() corelock.LeaseStore { return NewStore() }

// NewStore costruisce lo store nel suo tipo concreto, per i test che devono controllare l'orologio.
func NewStore() *Store {
	return &Store{leases: make(map[string]lease), now: time.Now}
}

// SetClock sostituisce l'orologio dello store. È per i test: fa scadere un lease senza attendere
// il TTL, che è l'unico modo di provare la scadenza in un tempo ragionevole.
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// heldLocked dice se la chiave è tenuta da qualcuno in questo istante. Va chiamata col lock preso.
//
// Un lease scaduto è come se non ci fosse: è la stessa regola che i backend reali applicano nel
// loro predicato di scadenza.
func (s *Store) heldLocked(key string) (lease, bool) {
	l, ok := s.leases[key]
	if !ok {
		return lease{}, false
	}
	if !l.expiresAt.After(s.now()) {
		return lease{}, false
	}
	return l, true
}

func (s *Store) TryAcquire(_ context.Context, key, token string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, held := s.heldLocked(key); held {
		return corelock.ErrNotAcquired
	}
	s.leases[key] = lease{token: token, expiresAt: s.now().Add(ttl)}
	return nil
}

func (s *Store) Renew(_ context.Context, key, token string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	l, held := s.heldLocked(key)
	if !held || l.token != token {
		return liberr.Tech(corelock.CodeRenew).
			WithMessage("lease non più posseduto: " + key).WithCause(corelock.ErrLockLost)
	}
	s.leases[key] = lease{token: token, expiresAt: s.now().Add(ttl)}
	return nil
}

func (s *Store) Release(_ context.Context, key, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if l, ok := s.leases[key]; ok && l.token == token {
		delete(s.leases, key)
	}
	return nil
}

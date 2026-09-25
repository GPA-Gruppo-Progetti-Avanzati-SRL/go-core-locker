// Package redisstore tiene i lease su Redis: è il backend Redis di go-core-locker.
//
//	corelock.Module(&svc.Lock, corelock.WithBackend(redisstore.Module))
//
// Una chiave per lease, col token come valore e la scadenza nativa di Redis (`PX`). La scadenza la
// misura quindi il server, come negli altri backend.
//
// # Perché non redsync
//
// L'implementazione storica passava da redsync, ma con un solo client `redsync.New(pool)` non
// esegue l'algoritmo Redlock: fa un `SET NX`, esattamente come queste venti righe. La dipendenza
// non comprava il quorum che il suo nome suggeriva, e imponeva una semantica sua — un'attesa fra i
// tentativi casuale fra 50 e 250 ms, e un Release che segnalava errore sul lock già perso — diversa
// da quella degli altri due backend. Il vero Redlock multi-nodo, se servirà, è un backend a parte:
// ha bisogno di più istanze indipendenti, che è una scelta di infrastruttura, non un dettaglio di
// questa.
package redisstore

import (
	"context"
	"errors"
	"time"

	core "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-app"
	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
	goredis "github.com/redis/go-redis/v9"
)

// I due script confrontano il token prima di agire: è ciò che impedisce a un proprietario di
// rinnovare o liberare il lock di un altro. Sono atomici per costruzione, essendo script.
var (
	renewScript = goredis.NewScript(`
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("PEXPIRE", KEYS[1], ARGV[2])
		end
		return 0`)

	releaseScript = goredis.NewScript(`
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("DEL", KEYS[1])
		end
		return 0`)
)

// Store è il LeaseStore su Redis.
type Store struct {
	client goredis.Cmdable
}

// New costruisce lo store sul client Redis dell'applicazione.
func New(client *goredis.Client) corelock.LeaseStore { return &Store{client: client} }

// NewWithClient costruisce lo store su qualsiasi client go-redis — cluster, o quello di un test.
func NewWithClient(c goredis.Cmdable) corelock.LeaseStore { return &Store{client: c} }

func (s *Store) TryAcquire(ctx context.Context, key, token string, ttl time.Duration) error {
	// SET NX PX: scrive solo se la chiave non esiste, e la scadenza la applica il server. Una
	// chiave scaduta non esiste più, quindi "libera o scaduta" è la stessa condizione.
	ok, err := s.client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return core.TechnicalError().WithAmbit(corelock.Ambit).WithCode(corelock.CodeAcquire).WithCause(err)
	}
	if !ok {
		return corelock.ErrNotAcquired
	}
	return nil
}

func (s *Store) Renew(ctx context.Context, key, token string, ttl time.Duration) error {
	res, err := renewScript.Run(ctx, s.client, []string{key}, token, ttl.Milliseconds()).Int64()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return core.TechnicalError().WithAmbit(corelock.Ambit).WithCode(corelock.CodeRenew).WithCause(err)
	}
	if res != 1 {
		return core.TechnicalError().WithAmbit(corelock.Ambit).WithCode(corelock.CodeRenew).
			WithMessage("lease non più posseduto: " + key).WithCause(corelock.ErrLockLost)
	}
	return nil
}

func (s *Store) Release(ctx context.Context, key, token string) error {
	// Il risultato non si guarda: liberare una chiave che non si possiede più non è un errore, e
	// l'unico sito di chiamata è un defer che non avrebbe alternative da scegliere.
	if _, err := releaseScript.Run(ctx, s.client, []string{key}, token).Int64(); err != nil &&
		!errors.Is(err, goredis.Nil) {
		return core.TechnicalError().WithAmbit(corelock.Ambit).WithCode(corelock.CodeRelease).WithCause(err)
	}
	return nil
}

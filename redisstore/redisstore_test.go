package redisstore_test

import (
	"testing"
	"time"

	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/conformance"
	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/redisstore"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// miniredis è un Redis vero abbastanza: parla il protocollo, esegue gli script Lua e applica le
// scadenze. La suite gira quindi in CI senza infrastruttura, come per il backend SQL.
//
// Una cosa non la fa da sé: miniredis non avanza il tempo da solo, quindi il test deve dirglielo.
func newStore(t *testing.T) corelock.LeaseStore {
	t.Helper()

	mr := miniredis.RunT(t)
	// La suite prova la scadenza attendendo davvero: qui l'attesa va tradotta in un avanzamento
	// dell'orologio di miniredis, altrimenti le chiavi non scadono mai.
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				mr.FastForward(20 * time.Millisecond)
			}
		}
	}()

	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return redisstore.NewWithClient(client)
}

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) corelock.LeaseStore { return newStore(t) })
}

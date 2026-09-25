// Package conformance è la suite che ogni backend di go-core-locker deve superare.
//
// Esiste perché i tre backend storici divergevano su scadenza, token e Release senza che nulla lo
// rilevasse: nessuno di loro aveva un test sulla semantica del lock. Una suite unica, eseguita
// identica da tutti, è l'unica cosa che impedisce a un backend nuovo — o a una modifica di uno
// vecchio — di rispondere diversamente alla stessa domanda.
//
// Un backend la esegue in tre righe:
//
//	func TestConformance(t *testing.T) {
//	    conformance.Run(t, func(t *testing.T) corelock.LeaseStore { return newStoreForTest(t) })
//	}
//
// La scadenza si prova con TTL brevi e attese reali, non con un orologio finto: è l'unico modo che
// funziona anche dove il tempo lo misura il server (Mongo, SQL, Redis), ed è esattamente ciò che si
// vuole verificare.
package conformance

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
)

// TTL usato dai test che devono attendere una scadenza reale. Abbastanza corto da non rallentare la
// suite, abbastanza lungo da non scadere per caso mentre il test fa altro.
const (
	shortTTL = 300 * time.Millisecond
	// grace è l'attesa aggiuntiva dopo shortTTL, per coprire la latenza del backend.
	grace = 200 * time.Millisecond
)

// NewStoreFunc costruisce uno store pulito per un singolo test.
type NewStoreFunc func(t *testing.T) corelock.LeaseStore

var keySeq atomic.Uint64

// uniqueKey dà a ogni test una chiave sua. I backend reali sono condivisi fra i test — e a volte
// fra esecuzioni diverse — quindi una chiave fissa li farebbe interferire.
func uniqueKey(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("conformance-%d-%d", time.Now().UnixNano(), keySeq.Add(1))
}

func newLocker(t *testing.T, newStore NewStoreFunc) corelock.Locker {
	t.Helper()
	return corelock.NewLocker(newStore(t), &corelock.Config{TTL: shortTTL})
}

// Run esegue la suite completa contro il backend indicato.
func Run(t *testing.T, newStore NewStoreFunc) {
	t.Helper()

	t.Run("AcquisisceUnaChiaveLibera", func(t *testing.T) { testAcquisisce(t, newStore) })
	t.Run("ContesaNonAcquisisce", func(t *testing.T) { testContesa(t, newStore) })
	t.Run("ChiaviIndipendenti", func(t *testing.T) { testChiaviIndipendenti(t, newStore) })
	t.Run("ReleaseLiberaLaChiave", func(t *testing.T) { testReleaseLibera(t, newStore) })
	t.Run("ReleaseEIdempotente", func(t *testing.T) { testReleaseIdempotente(t, newStore) })
	t.Run("ReleaseNonLiberaIlLockAltrui", func(t *testing.T) { testReleaseNonRuba(t, newStore) })
	t.Run("ScadenzaRendeLaChiaveRiacquisibile", func(t *testing.T) { testScadenza(t, newStore) })
	t.Run("ExtendMantieneIlLock", func(t *testing.T) { testExtend(t, newStore) })
	t.Run("ExtendSuLockPersoRitornaErrLockLost", func(t *testing.T) { testExtendPerso(t, newStore) })
	t.Run("RetryAcquisisceDopoIlRilascio", func(t *testing.T) { testRetryRiesce(t, newStore) })
	t.Run("RetryEsauritoRitornaErrNotAcquired", func(t *testing.T) { testRetryEsaurito(t, newStore) })
	t.Run("ContextAnnullatoInterrompeLAttesa", func(t *testing.T) { testContextAnnullato(t, newStore) })
}

func testAcquisisce(t *testing.T, newStore NewStoreFunc) {
	l, ctx, key := newLocker(t, newStore), context.Background(), uniqueKey(t)

	h, err := l.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire su chiave libera: %v", err)
	}
	if h == nil {
		t.Fatal("Acquire riuscita ma Handle nullo")
	}
	if err := h.Release(ctx); err != nil {
		t.Errorf("Release: %v", err)
	}
}

func testContesa(t *testing.T, newStore NewStoreFunc) {
	l, ctx, key := newLocker(t, newStore), context.Background(), uniqueKey(t)

	h, err := l.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("prima Acquire: %v", err)
	}
	defer func() { _ = h.Release(ctx) }()

	// Senza opzioni: un tentativo solo, non bloccante.
	if _, err := l.Acquire(ctx, key); !errors.Is(err, corelock.ErrNotAcquired) {
		t.Fatalf("seconda Acquire = %v, atteso ErrNotAcquired", err)
	}
}

func testChiaviIndipendenti(t *testing.T, newStore NewStoreFunc) {
	l, ctx := newLocker(t, newStore), context.Background()
	a, b := uniqueKey(t), uniqueKey(t)

	ha, err := l.Acquire(ctx, a)
	if err != nil {
		t.Fatalf("Acquire(%s): %v", a, err)
	}
	defer func() { _ = ha.Release(ctx) }()

	hb, err := l.Acquire(ctx, b)
	if err != nil {
		t.Fatalf("Acquire(%s) mentre %s è tenuta: %v — le chiavi non devono interferire", b, a, err)
	}
	_ = hb.Release(ctx)
}

func testReleaseLibera(t *testing.T, newStore NewStoreFunc) {
	l, ctx, key := newLocker(t, newStore), context.Background(), uniqueKey(t)

	h, err := l.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := h.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}

	h2, err := l.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire dopo Release: %v — la chiave doveva essere libera", err)
	}
	_ = h2.Release(ctx)
}

func testReleaseIdempotente(t *testing.T, newStore NewStoreFunc) {
	l, ctx, key := newLocker(t, newStore), context.Background(), uniqueKey(t)

	h, err := l.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := h.Release(ctx); err != nil {
		t.Fatalf("prima Release: %v", err)
	}
	// Il secondo rilascio non ha nulla da liberare: non è un errore. Il solo sito di chiamata è un
	// defer, che di fronte a un errore qui non avrebbe alternative da scegliere.
	if err := h.Release(ctx); err != nil {
		t.Errorf("seconda Release = %v, atteso nil: Release deve essere idempotente", err)
	}
}

func testReleaseNonRuba(t *testing.T, newStore NewStoreFunc) {
	l, ctx, key := newLocker(t, newStore), context.Background(), uniqueKey(t)

	// Il primo proprietario perde il lock per scadenza.
	stale, err := l.Acquire(ctx, key, corelock.WithExpiry(shortTTL))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	time.Sleep(shortTTL + grace)

	// Il secondo lo prende.
	fresh, err := l.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire dopo la scadenza: %v", err)
	}
	defer func() { _ = fresh.Release(ctx) }()

	// Il primo rilascia: non deve toccare il lock del secondo.
	if err := stale.Release(ctx); err != nil {
		t.Errorf("Release del proprietario scaduto = %v, atteso nil", err)
	}
	if _, err := l.Acquire(ctx, key); !errors.Is(err, corelock.ErrNotAcquired) {
		t.Fatalf("la chiave è libera dopo il Release di un altro proprietario: "+
			"Release deve verificare il token (err = %v)", err)
	}
}

func testScadenza(t *testing.T, newStore NewStoreFunc) {
	l, ctx, key := newLocker(t, newStore), context.Background(), uniqueKey(t)

	if _, err := l.Acquire(ctx, key, corelock.WithExpiry(shortTTL)); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	time.Sleep(shortTTL + grace)

	h, err := l.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire dopo la scadenza = %v: un lease scaduto non tiene più la chiave", err)
	}
	_ = h.Release(ctx)
}

func testExtend(t *testing.T, newStore NewStoreFunc) {
	l, ctx, key := newLocker(t, newStore), context.Background(), uniqueKey(t)

	h, err := l.Acquire(ctx, key, corelock.WithExpiry(shortTTL))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release(ctx) }()

	// Rinnova a metà TTL, poi attende oltre la scadenza originale.
	time.Sleep(shortTTL / 2)
	if err := h.Extend(ctx); err != nil {
		t.Fatalf("Extend su lock tenuto: %v", err)
	}
	time.Sleep(shortTTL/2 + 50*time.Millisecond)

	if _, err := l.Acquire(ctx, key); !errors.Is(err, corelock.ErrNotAcquired) {
		t.Fatalf("la chiave è libera dopo Extend (err = %v): il rinnovo non ha spostato la scadenza", err)
	}
}

func testExtendPerso(t *testing.T, newStore NewStoreFunc) {
	l, ctx, key := newLocker(t, newStore), context.Background(), uniqueKey(t)

	h, err := l.Acquire(ctx, key, corelock.WithExpiry(shortTTL))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	time.Sleep(shortTTL + grace)

	// Un altro proprietario prende la chiave: il rinnovo del primo non deve riuscire.
	other, err := l.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire dopo la scadenza: %v", err)
	}
	defer func() { _ = other.Release(ctx) }()

	if err := h.Extend(ctx); !errors.Is(err, corelock.ErrLockLost) {
		t.Fatalf("Extend su lock perso = %v, atteso ErrLockLost: è il segnale che la sezione "+
			"critica sta girando senza protezione", err)
	}
}

func testRetryRiesce(t *testing.T, newStore NewStoreFunc) {
	l, ctx, key := newLocker(t, newStore), context.Background(), uniqueKey(t)

	h, err := l.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = h.Release(ctx)
		close(released)
	}()

	start := time.Now()
	h2, err := l.Acquire(ctx, key, corelock.WithWait(3*time.Second, 50*time.Millisecond))
	if err != nil {
		t.Fatalf("Acquire con attesa = %v: doveva riuscire dopo il rilascio", err)
	}
	defer func() { _ = h2.Release(ctx) }()

	if time.Since(start) < 100*time.Millisecond {
		t.Error("Acquire con attesa è tornata prima del rilascio: non ha atteso")
	}
	<-released
}

func testRetryEsaurito(t *testing.T, newStore NewStoreFunc) {
	l, ctx, key := newLocker(t, newStore), context.Background(), uniqueKey(t)

	h, err := l.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release(ctx) }()

	start := time.Now()
	_, err = l.Acquire(ctx, key, corelock.WithTries(3), corelock.WithRetryDelay(30*time.Millisecond))
	if !errors.Is(err, corelock.ErrNotAcquired) {
		t.Fatalf("= %v, atteso ErrNotAcquired a tentativi esauriti", err)
	}
	// Tre tentativi ⇒ due attese fra l'uno e l'altro.
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("tornata dopo %v: non ha atteso fra i tentativi", elapsed)
	}
}

func testContextAnnullato(t *testing.T, newStore NewStoreFunc) {
	l, key := newLocker(t, newStore), uniqueKey(t)
	ctx := context.Background()

	h, err := l.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = h.Release(ctx) }()

	waitCtx, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err = l.Acquire(waitCtx, key, corelock.WithWait(10*time.Second, 40*time.Millisecond))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("= %v, atteso context.Canceled: l'attesa deve osservare il context", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("l'attesa è durata %v: il context non l'ha interrotta", elapsed)
	}
}

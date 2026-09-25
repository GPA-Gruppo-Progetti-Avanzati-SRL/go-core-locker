package sqlstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/conformance"
	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/sqlstore"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	_ "modernc.org/sqlite"
)

var dbSeq atomic.Uint64

// newDB apre un SQLite in memoria condiviso fra le connessioni del pool. Senza `cache=shared` ogni
// connessione vedrebbe un database suo, e il lock non avrebbe nulla da condividere.
func newDB(t *testing.T) *bun.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:locker%d?mode=memory&cache=shared", dbSeq.Add(1))
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("apertura sqlite: %v", err)
	}
	// Il database in memoria vive finché resta aperta almeno una connessione.
	sqldb.SetMaxIdleConns(2)

	db := bun.NewDB(sqldb, sqlitedialect.New())
	t.Cleanup(func() { _ = db.Close() })

	if err := sqlstore.EnsureSchema(context.Background(), db, nil); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return db
}

// La suite di conformità gira sul backend SQL con SQLite in memoria: nessuna infrastruttura, quindi
// gira sempre, anche in CI. Il dialetto è uno solo dei tre, ma è quello che copre il ramo
// PostgreSQL/SQLite dell'upsert — MySQL ha il suo, e va provato con un database vero.
func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) corelock.LeaseStore {
		return sqlstore.NewWithDB(newDB(t), nil)
	})
}

// La scadenza deve valutarla il database, non il processo: è l'invariante che rende il lock un lock
// quando gli orologi delle repliche non coincidono. Qui si verifica che la riga scritta porti una
// scadenza calcolata dal database e non passata da Go.
func TestScadenzaCalcolataDalDatabase(t *testing.T) {
	db := newDB(t)
	store := sqlstore.NewWithDB(db, nil)
	ctx := context.Background()

	if err := store.TryAcquire(ctx, "k", "tok", 30_000_000_000); err != nil { // 30s
		t.Fatalf("TryAcquire: %v", err)
	}

	var expires, now int64
	err := db.QueryRow(
		`SELECT expires_at_ms, CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER) FROM scheduler_locks WHERE lock_key = 'k'`,
	).Scan(&expires, &now)
	if err != nil {
		t.Fatalf("lettura della riga: %v", err)
	}

	delta := expires - now
	if delta < 25_000 || delta > 35_000 {
		t.Errorf("scadenza a %d ms da adesso, attesi ~30000: non è stata calcolata dal database", delta)
	}
}

func TestEnsureSchema_Idempotente(t *testing.T) {
	db := newDB(t) // già creato una volta
	if err := sqlstore.EnsureSchema(context.Background(), db, nil); err != nil {
		t.Errorf("seconda EnsureSchema: %v", err)
	}
}

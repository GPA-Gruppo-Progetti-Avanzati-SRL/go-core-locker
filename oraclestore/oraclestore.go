// Package oraclestore tiene i lease in una tabella Oracle (19c+): è il gemello di sqlstore per
// Oracle, con cui non condivide import — un'app Oracle non compila il codice degli altri dialetti.
//
//	corelock.Module(&svc.Lock, corelock.WithBackend(oraclestore.Module))
//	batch.Module(&svc.Batch, Register, batch.WithLocker(oraclestore.Module), ...)
//
// Rispetto a sqlstore cambiano l'upsert, che su Oracle è una MERGE (non c'è ON CONFLICT), e
// l'istante corrente, che Oracle non dà in millisecondi epoch. La tabella la crea EnsureSchema,
// oppure la migrazione dell'applicazione, con i nomi minuscoli quotati che bun scrive dal modello.
package oraclestore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-app"

	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/internal/sqllease"
	coresql "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-sql"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Module registra il backend Oracle. Si passa a corelock.WithBackend (o a batch.WithLocker) per
// riferimento diretto. Richiede il *coresql.Service dell'applicazione su Oracle: un altro
// dialetto ferma l'avvio.
func Module(modes ...string) {
	core.ProvideAs[corelock.LeaseStore](New, modes...)
}

// New costruisce lo store sul servizio SQL dell'applicazione, che deve essere su Oracle.
func New(svc *coresql.Service, cfg *corelock.Config) (corelock.LeaseStore, error) {
	if name := svc.DB().Dialect().Name(); name != dialect.Oracle {
		return nil, sqllease.Errs.Tech(corelock.CodeDialect).WithMessage(fmt.Sprintf(
			"go-core-locker oraclestore: il database è %s, non Oracle (per PostgreSQL/MySQL/SQLite c'è sqlstore)", name))
	}
	return NewWithDB(svc.IDB(), cfg), nil
}

// NewWithDB costruisce lo store su un handle bun su Oracle. È la forma che usano i test.
func NewWithDB(db bun.IDB, cfg *corelock.Config) corelock.LeaseStore {
	return sqllease.New(db, cfg, oracleDialect{})
}

// EnsureSchema crea la tabella dei lease se non esiste.
//
// IF NOT EXISTS arriva solo con la 23ai e la minima supportata è la 19c: la tabella si crea senza,
// e ORA-00955 (nome già usato) vale come "esiste già".
func EnsureSchema(ctx context.Context, db *bun.DB, cfg *corelock.Config) *core.Error {
	return sqllease.EnsureSchema(ctx, db, cfg, oracleDialect{})
}

type oracleDialect struct{}

// NowMs: Oracle non ha un epoch, ma la differenza fra due TIMESTAMP è un INTERVAL DAY TO SECOND,
// che si scompone. SYS_EXTRACT_UTC perché SYSTIMESTAMP porta il fuso del server; dentro
// un'istruzione SYSTIMESTAMP è costante, quindi le quattro EXTRACT leggono lo stesso istante.
func (oracleDialect) NowMs() string { return nowMs }

const nowMs = "(EXTRACT(DAY FROM (SYS_EXTRACT_UTC(SYSTIMESTAMP) - TIMESTAMP '1970-01-01 00:00:00')) * 86400000" +
	" + EXTRACT(HOUR FROM (SYS_EXTRACT_UTC(SYSTIMESTAMP) - TIMESTAMP '1970-01-01 00:00:00')) * 3600000" +
	" + EXTRACT(MINUTE FROM (SYS_EXTRACT_UTC(SYSTIMESTAMP) - TIMESTAMP '1970-01-01 00:00:00')) * 60000" +
	" + ROUND(EXTRACT(SECOND FROM (SYS_EXTRACT_UTC(SYSTIMESTAMP) - TIMESTAMP '1970-01-01 00:00:00')) * 1000))"

// TryAcquire è l'upsert condizionato su Oracle: una MERGE la cui UPDATE scatta solo sul lease
// scaduto. Zero righe toccate = la chiave è di un altro.
//
// Due MERGE concorrenti sulla stessa chiave ancora assente possono prendere entrambe il ramo
// NOT MATCHED: la seconda fallisce con ORA-00001 sulla chiave primaria, ed è la stessa risposta —
// il lease ce l'ha l'altro.
func (oracleDialect) TryAcquire(ctx context.Context, db bun.IDB, table bun.Ident, key, token string, ttl time.Duration) error {
	col := sqllease.Col
	res, err := db.NewRaw(`
		MERGE INTO ? l
		USING (SELECT ? AS k, ? AS o, `+nowMs+` + ? AS e FROM dual) src
		ON (l.? = src.k)
		WHEN MATCHED THEN UPDATE SET l.? = src.o, l.? = src.e WHERE l.? <= `+nowMs+`
		WHEN NOT MATCHED THEN INSERT (?, ?, ?) VALUES (src.k, src.o, src.e)
	`, table, key, token, ttl.Milliseconds(),
		col("lock_key"),
		col("owner"), col("expires_at_ms"), col("expires_at_ms"),
		col("lock_key"), col("owner"), col("expires_at_ms"),
	).Exec(ctx)
	if isOracleError(err, "ORA-00001") {
		return corelock.ErrNotAcquired
	}
	if err != nil {
		return sqllease.Errs.Tech(corelock.CodeAcquire).WithCause(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return sqllease.Errs.Tech(corelock.CodeAcquire).WithCause(err)
	}
	if n == 0 {
		return corelock.ErrNotAcquired
	}
	return nil
}

func (oracleDialect) CreateTable(ctx context.Context, q *bun.CreateTableQuery) error {
	_, err := q.Exec(ctx)
	if isOracleError(err, "ORA-00955") {
		return nil
	}
	return err
}

// isOracleError dice se err è l'errore Oracle code (es. "ORA-00001"). Si riconosce dal testo, che
// è lo stesso con go-ora e con godror: importare un driver per il suo tipo d'errore imporrebbe
// quel driver all'applicazione.
func isOracleError(err error, code string) bool {
	return err != nil && strings.Contains(err.Error(), code)
}

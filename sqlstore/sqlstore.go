// Package sqlstore tiene i lease in una tabella relazionale: è il backend SQL di go-core-locker per
// PostgreSQL, MySQL e SQLite. Per Oracle c'è oraclestore: i due package non si importano a
// vicenda, quindi un'app Postgres non compila il codice Oracle.
//
//	corelock.Module(&svc.Lock, corelock.WithBackend(sqlstore.Module))
//
// La tabella la crea EnsureSchema, oppure la migrazione dell'applicazione.
package sqlstore

import (
	"context"
	"time"

	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-app"

	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/internal/sqllease"
	coresql "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-sql"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// New costruisce lo store sul servizio SQL dell'applicazione. Su Oracle ferma l'avvio: lì l'upsert
// di questo store (ON CONFLICT) non esiste, e il backend giusto è oraclestore.
func New(svc *coresql.Service, cfg *corelock.Config) (corelock.LeaseStore, error) {
	if name := svc.DB().Dialect().Name(); name == dialect.Oracle {
		return nil, sqllease.Errs.Tech(corelock.CodeDialect).WithMessage(
			"go-core-locker sqlstore: il database è Oracle, che ha il suo backend (oraclestore)")
	}
	return NewWithDB(svc.IDB(), cfg), nil
}

// NewWithDB costruisce lo store su un handle bun qualsiasi. È la forma che usano i test e chi ha
// già un *bun.DB in mano senza passare dal Service.
func NewWithDB(db bun.IDB, cfg *corelock.Config) corelock.LeaseStore {
	return sqllease.New(db, cfg, sqlDialect{name: db.Dialect().Name()})
}

// EnsureSchema crea la tabella dei lease se non esiste.
//
// È esplicita e non automatica al boot: creare tabelle è una modifica allo schema, e dove le
// migrazioni sono governate deve restare una scelta di chi le governa.
func EnsureSchema(ctx context.Context, db *bun.DB, cfg *corelock.Config) *core.Error {
	return sqllease.EnsureSchema(ctx, db, cfg, sqlDialect{name: db.Dialect().Name()})
}

// sqlDialect è il lease su PostgreSQL, MySQL e SQLite: cambiano solo l'istante corrente e, su
// MySQL, la forma dell'upsert.
type sqlDialect struct {
	name dialect.Name
}

// NowMs è l'unico punto in cui il dialetto conta davvero: tutto il resto della semantica del lock
// è del motore. Il default segue PostgreSQL, che è la sintassi standard fra quelle qui elencate.
func (d sqlDialect) NowMs() string {
	switch d.name {
	case dialect.MySQL:
		return "CAST(UNIX_TIMESTAMP(NOW(3)) * 1000 AS SIGNED)"
	case dialect.SQLite:
		return "CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)"
	default: // PostgreSQL e compatibili
		return "CAST(EXTRACT(EPOCH FROM CURRENT_TIMESTAMP) * 1000 AS BIGINT)"
	}
}

func (d sqlDialect) TryAcquire(ctx context.Context, db bun.IDB, table bun.Ident, key, token string, ttl time.Duration) error {
	now := d.NowMs()
	row := &sqllease.Row{Key: key, Owner: token}

	q := db.NewInsert().Model(row).
		ModelTableExpr("? AS l", table).
		Value("expires_at_ms", now+" + ?", ttl.Milliseconds())

	if d.name == dialect.MySQL {
		// MySQL non ha la WHERE sull'upsert: la condizione di scadenza entra nelle assegnazioni.
		q = q.On("DUPLICATE KEY UPDATE owner = IF(expires_at_ms <= " + now + ", VALUES(owner), owner), " +
			"expires_at_ms = IF(expires_at_ms <= " + now + ", VALUES(expires_at_ms), expires_at_ms)")
	} else {
		q = q.On("CONFLICT (lock_key) DO UPDATE").
			Set("owner = EXCLUDED.owner").
			Set("expires_at_ms = EXCLUDED.expires_at_ms").
			Where("l.expires_at_ms <= " + now)
	}

	res, err := q.Exec(ctx)
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

func (sqlDialect) CreateTable(ctx context.Context, q *bun.CreateTableQuery) error {
	_, err := q.IfNotExists().Exec(ctx)
	return err
}

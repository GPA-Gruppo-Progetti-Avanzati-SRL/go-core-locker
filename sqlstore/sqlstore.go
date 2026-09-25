// Package sqlstore tiene i lease in una tabella relazionale: è il backend SQL di go-core-locker.
//
//	corelock.Module(&svc.Lock, corelock.WithBackend(sqlstore.Module))
//
// La tabella la crea EnsureSchema, oppure la migrazione dell'applicazione.
package sqlstore

import (
	"context"
	"time"

	core "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-app"
	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
	coresql "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-sql"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// lockRow è la riga di lease.
//
// `lock_key` e non `key`, che è parola riservata. La scadenza è un **intero di millisecondi** e non
// un timestamp: dev'essere il database a decidere se un lease è scaduto (vedi corelock.LeaseStore),
// e il confronto fra un timestamp e `CURRENT_TIMESTAMP` non è portabile — su SQLite bun scrive
// l'istante in un formato che non si confronta lessicograficamente con quello di `CURRENT_TIMESTAMP`.
// Due interi si confrontano allo stesso modo ovunque.
type lockRow struct {
	bun.BaseModel `bun:"table:scheduler_locks"`

	Key         string `bun:"lock_key,pk"`
	Owner       string `bun:"owner,notnull"`
	ExpiresAtMs int64  `bun:"expires_at_ms,notnull"`
}

// Store è il LeaseStore su SQL.
type Store struct {
	db    bun.IDB
	table string
}

// New costruisce lo store sul servizio SQL dell'applicazione.
func New(svc *coresql.Service, cfg *corelock.Config) corelock.LeaseStore {
	return NewWithDB(svc.IDB(), cfg)
}

// NewWithDB costruisce lo store su un handle bun qualsiasi. È la forma che usano i test e chi ha
// già un *bun.DB in mano senza passare dal Service.
func NewWithDB(db bun.IDB, cfg *corelock.Config) corelock.LeaseStore {
	c := (&corelock.Config{}).WithDefaults()
	if cfg != nil {
		c = cfg.WithDefaults()
	}
	return &Store{db: db, table: c.Sql.Table}
}

// nowMs è l'espressione che dà al database l'istante corrente in millisecondi.
//
// È l'unico punto in cui il dialetto conta davvero: tutto il resto della semantica del lock è del
// motore. Il default segue PostgreSQL, che è la sintassi standard fra quelle qui elencate.
func nowMs(d dialect.Name) string {
	switch d {
	case dialect.MySQL:
		return "CAST(UNIX_TIMESTAMP(NOW(3)) * 1000 AS SIGNED)"
	case dialect.SQLite:
		return "CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)"
	default: // PostgreSQL e compatibili
		return "CAST(EXTRACT(EPOCH FROM CURRENT_TIMESTAMP) * 1000 AS BIGINT)"
	}
}

func (s *Store) tableExpr() bun.Ident { return bun.Ident(s.table) }

func (s *Store) TryAcquire(ctx context.Context, key, token string, ttl time.Duration) error {
	now := nowMs(s.db.Dialect().Name())
	row := &lockRow{Key: key, Owner: token}

	q := s.db.NewInsert().Model(row).
		ModelTableExpr("? AS l", s.tableExpr()).
		Value("expires_at_ms", now+" + ?", ttl.Milliseconds())

	if s.db.Dialect().Name() == dialect.MySQL {
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
		return core.TechnicalError().WithAmbit(corelock.Ambit).WithCode(corelock.CodeAcquire).WithCause(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return core.TechnicalError().WithAmbit(corelock.Ambit).WithCode(corelock.CodeAcquire).WithCause(err)
	}
	if n == 0 {
		return corelock.ErrNotAcquired
	}
	return nil
}

func (s *Store) Renew(ctx context.Context, key, token string, ttl time.Duration) error {
	now := nowMs(s.db.Dialect().Name())

	res, err := s.db.NewUpdate().Model((*lockRow)(nil)).
		ModelTableExpr("? AS l", s.tableExpr()).
		Set("expires_at_ms = "+now+" + ?", ttl.Milliseconds()).
		Where("lock_key = ?", key).
		Where("owner = ?", token).
		// Un lease già scaduto è perso anche se nessuno l'ha ancora rubato: rinnovarlo
		// resusciterebbe un lock che la sezione critica non aveva più.
		Where("expires_at_ms > " + now).
		Exec(ctx)
	if err != nil {
		return core.TechnicalError().WithAmbit(corelock.Ambit).WithCode(corelock.CodeRenew).WithCause(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return core.TechnicalError().WithAmbit(corelock.Ambit).WithCode(corelock.CodeRenew).WithCause(err)
	}
	if n == 0 {
		return core.TechnicalError().WithAmbit(corelock.Ambit).WithCode(corelock.CodeRenew).
			WithMessage("lease non più posseduto: " + key).WithCause(corelock.ErrLockLost)
	}
	return nil
}

func (s *Store) Release(ctx context.Context, key, token string) error {
	_, err := s.db.NewDelete().Model((*lockRow)(nil)).
		ModelTableExpr("? AS l", s.tableExpr()).
		Where("lock_key = ?", key).
		Where("owner = ?", token).
		Exec(ctx)
	if err != nil {
		return core.TechnicalError().WithAmbit(corelock.Ambit).WithCode(corelock.CodeRelease).WithCause(err)
	}
	return nil
}

// EnsureSchema crea la tabella dei lease se non esiste.
//
// È esplicita e non automatica al boot: creare tabelle è una modifica allo schema, e dove le
// migrazioni sono governate deve restare una scelta di chi le governa.
func EnsureSchema(ctx context.Context, db *bun.DB, cfg *corelock.Config) *core.ApplicationError {
	c := (&corelock.Config{}).WithDefaults()
	if cfg != nil {
		c = cfg.WithDefaults()
	}
	_, err := db.NewCreateTable().Model((*lockRow)(nil)).
		ModelTableExpr("?", bun.Ident(c.Sql.Table)).
		IfNotExists().Exec(ctx)
	if err != nil {
		return core.TechnicalError().WithAmbit(corelock.Ambit).WithCode(corelock.CodeSchema).WithCause(err)
	}
	return nil
}

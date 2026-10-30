// Package sqllease è il motore comune dei backend relazionali di go-core-locker: la riga di lease,
// Renew, Release e la creazione della tabella. Ciò che cambia da un database all'altro — l'istante
// corrente in millisecondi e l'upsert condizionato di TryAcquire — è un Dialect, e lo portano i
// package pubblici (sqlstore per PostgreSQL/MySQL/SQLite, oraclestore per Oracle), che non si
// importano a vicenda.
package sqllease

import (
	"context"
	"time"

	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-app"
	"github.com/uptrace/bun"

	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
)

// Errs costruisce gli errori dei backend con l'ambito della libreria (vedi core.AmbitErrors).
var Errs = core.AmbitErrors{Ambit: corelock.Ambit}

// Row è la riga di lease.
//
// `lock_key` e non `key`, che è parola riservata. La scadenza è un **intero di millisecondi** e non
// un timestamp: dev'essere il database a decidere se un lease è scaduto (vedi corelock.LeaseStore),
// e il confronto fra un timestamp e `CURRENT_TIMESTAMP` non è portabile — su SQLite bun scrive
// l'istante in un formato che non si confronta lessicograficamente con quello di `CURRENT_TIMESTAMP`.
// Due interi si confrontano allo stesso modo ovunque.
type Row struct {
	bun.BaseModel `bun:"table:scheduler_locks"`

	Key         string `bun:"lock_key,pk"`
	Owner       string `bun:"owner,notnull"`
	ExpiresAtMs int64  `bun:"expires_at_ms,notnull"`
}

// Dialect è la parte del lease che dipende dal database.
type Dialect interface {
	// NowMs è l'espressione che dà al database l'istante corrente in millisecondi.
	NowMs() string
	// TryAcquire è l'upsert condizionato: prende la chiave se è libera o scaduta, altrimenti
	// ritorna corelock.ErrNotAcquired.
	TryAcquire(ctx context.Context, db bun.IDB, table bun.Ident, key, token string, ttl time.Duration) error
	// CreateTable esegue la CREATE TABLE in modo idempotente (con IF NOT EXISTS, o riconoscendo
	// l'errore "esiste già" dove IF NOT EXISTS non c'è).
	CreateTable(ctx context.Context, q *bun.CreateTableQuery) error
}

// Col quota un nome di colonna col quoting del dialetto. Non è cosmetico: bun quota sempre gli
// identificatori che deduce dal modello (CREATE TABLE, INSERT), e su Oracle un nome quotato è
// case-sensitive — `lock_key` non quotato diventa LOCK_KEY e non trova la colonna "lock_key".
// Su PostgreSQL, SQLite e MySQL quotato e non quotato coincidono.
func Col(name string) bun.Ident { return bun.Ident(name) }

// Store è il LeaseStore su SQL, per un Dialect.
type Store struct {
	db      bun.IDB
	table   string
	dialect Dialect
}

// New costruisce lo store su un handle bun e sulla config (nil = default).
func New(db bun.IDB, cfg *corelock.Config, d Dialect) *Store {
	return &Store{db: db, table: tableName(cfg), dialect: d}
}

func tableName(cfg *corelock.Config) string {
	c := (&corelock.Config{}).WithDefaults()
	if cfg != nil {
		c = cfg.WithDefaults()
	}
	return c.Sql.Table
}

func (s *Store) tableExpr() bun.Ident { return bun.Ident(s.table) }

func (s *Store) TryAcquire(ctx context.Context, key, token string, ttl time.Duration) error {
	return s.dialect.TryAcquire(ctx, s.db, s.tableExpr(), key, token, ttl)
}

func (s *Store) Renew(ctx context.Context, key, token string, ttl time.Duration) error {
	now := s.dialect.NowMs()

	// Niente alias di tabella: Oracle non accetta `AS` davanti all'alias di una tabella.
	res, err := s.db.NewUpdate().Model((*Row)(nil)).
		ModelTableExpr("?", s.tableExpr()).
		Set("? = "+now+" + ?", Col("expires_at_ms"), ttl.Milliseconds()).
		Where("? = ?", Col("lock_key"), key).
		Where("? = ?", Col("owner"), token).
		// Un lease già scaduto è perso anche se nessuno l'ha ancora rubato: rinnovarlo
		// resusciterebbe un lock che la sezione critica non aveva più.
		Where("? > "+now, Col("expires_at_ms")).
		Exec(ctx)
	if err != nil {
		return Errs.Tech(corelock.CodeRenew).WithCause(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Errs.Tech(corelock.CodeRenew).WithCause(err)
	}
	if n == 0 {
		return Errs.Tech(corelock.CodeRenew).
			WithMessage("lease non più posseduto: " + key).WithCause(corelock.ErrLockLost)
	}
	return nil
}

func (s *Store) Release(ctx context.Context, key, token string) error {
	_, err := s.db.NewDelete().Model((*Row)(nil)).
		ModelTableExpr("?", s.tableExpr()).
		Where("? = ?", Col("lock_key"), key).
		Where("? = ?", Col("owner"), token).
		Exec(ctx)
	if err != nil {
		return Errs.Tech(corelock.CodeRelease).WithCause(err)
	}
	return nil
}

// EnsureSchema crea la tabella dei lease se non esiste, nel modo del Dialect.
func EnsureSchema(ctx context.Context, db *bun.DB, cfg *corelock.Config, d Dialect) *core.Error {
	q := db.NewCreateTable().Model((*Row)(nil)).ModelTableExpr("?", bun.Ident(tableName(cfg)))
	if err := d.CreateTable(ctx, q); err != nil {
		return Errs.Tech(corelock.CodeSchema).WithCause(err)
	}
	return nil
}

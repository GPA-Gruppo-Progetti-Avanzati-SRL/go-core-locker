package oraclestore_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/conformance"
	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/oraclestore"
	_ "github.com/sijms/go-ora/v2"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/oracledialect"
)

// oraclestore (MERGE, epoch calcolato da SYSTIMESTAMP, niente IF NOT EXISTS) ha bisogno di un
// Oracle vero: senza ORACLE_URL i test si saltano. La versione minima supportata è la 19c, quindi
// il database di prova non deve essere una 23ai, che ha le sintassi che la 19c non ha.
//
//	ORACLE_URL=oracle://system:pw@localhost:1521/XEPDB1 go test ./oraclestore/...
const envOracleURL = "ORACLE_URL"

var oracleDB = sync.OnceValues(func() (*bun.DB, error) {
	sqldb, err := sql.Open("oracle", os.Getenv(envOracleURL))
	if err != nil {
		return nil, err
	}
	if err := sqldb.PingContext(context.Background()); err != nil {
		return nil, err
	}
	return bun.NewDB(sqldb, oracledialect.New()), nil
})

var oracleSeq int

// newOracleStore crea una tabella di lease per test: la suite non condivide stato fra le prove.
func newOracleStore(t *testing.T) corelock.LeaseStore {
	t.Helper()
	if os.Getenv(envOracleURL) == "" {
		t.Skip(envOracleURL + " non impostata: oraclestore richiede un Oracle")
	}
	db, err := oracleDB()
	if err != nil {
		t.Fatalf("Oracle: %v", err)
	}
	oracleSeq++
	cfg := &corelock.Config{Sql: corelock.SQLConfig{Table: fmt.Sprintf("locks_test_%d", oracleSeq)}}
	ctx := context.Background()
	if err := oraclestore.EnsureSchema(ctx, db, cfg); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	// La seconda chiamata prova il ramo ORA-00955: su 19c non c'è IF NOT EXISTS.
	if err := oraclestore.EnsureSchema(ctx, db, cfg); err != nil {
		t.Fatalf("seconda EnsureSchema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.NewDropTable().TableExpr("?", bun.Ident(cfg.Sql.Table)).Exec(context.Background())
	})
	return oraclestore.NewWithDB(db, cfg)
}

func TestConformance_Oracle(t *testing.T) {
	conformance.Run(t, newOracleStore)
}

package mongostore_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/conformance"
	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/mongostore"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// I test del backend Mongo hanno bisogno di un Mongo: senza MONGO_URL si saltano, invece di
// fallire in CI o di far credere che il backend sia coperto.
//
//	MONGO_URL=mongodb://localhost:27017 go test ./mongostore/...
const envURL = "MONGO_URL"

var dbSeq atomic.Uint64

// collections adatta un *mongo.Database all'interfaccia che lo store si aspettava dal Service.
type collections struct{ db *mongo.Database }

func (c collections) GetCollection(name, _ string) *mongo.Collection { return c.db.Collection(name) }

// newStore apre un database usa-e-getta e restituisce lo store insieme alla collection dei lease,
// perché i test che devono sporcare un documento ci arrivino senza reinventare la connessione.
func newStore(t *testing.T) (corelock.LeaseStore, *mongo.Collection) {
	t.Helper()

	uri := os.Getenv(envURL)
	if uri == "" {
		t.Skipf("%s non impostata: il backend Mongo non è coperto in questa esecuzione", envURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cli, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connessione a Mongo: %v", err)
	}
	if err := cli.Ping(ctx, nil); err != nil {
		t.Fatalf("ping di Mongo: %v", err)
	}

	dbName := fmt.Sprintf("corelock_test_%d_%d", time.Now().UnixNano(), dbSeq.Add(1))
	db := cli.Database(dbName)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = db.Drop(c)
		_ = cli.Disconnect(c)
	})

	svc := collections{db: db}
	if err := mongostore.EnsureSchema(ctx, svc, nil); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return mongostore.New(svc, nil), db.Collection(corelock.DefaultLeaseTable)
}

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) corelock.LeaseStore {
		store, _ := newStore(t)
		return store
	})
}

// Un documento con `expiresAt` assente o di tipo sbagliato deve valere come scaduto.
//
// Senza questa regola il predicato di scadenza non lo seleziona, l'upsert ricade sull'inserimento e
// sbatte su duplicate key: quella chiave resta inacquisibile per sempre, e il sintomo è soltanto un
// job che smette di partire.
func TestLeaseMalformatoNonBloccaLaChiave(t *testing.T) {
	store, coll := newStore(t)
	ctx := context.Background()

	for _, bad := range []any{nil, "non-una-data", 42} {
		if err := store.TryAcquire(ctx, "rotta", "tok", time.Minute); err != nil {
			t.Fatalf("TryAcquire iniziale: %v", err)
		}
		// La scadenza viene corrotta sotto i piedi del proprietario.
		if _, err := coll.UpdateOne(ctx, bson.M{"_id": "rotta"},
			bson.M{"$set": bson.M{"expiresAt": bad}}); err != nil {
			t.Fatalf("corruzione del documento: %v", err)
		}

		if err := store.TryAcquire(ctx, "rotta", "tok2", time.Minute); err != nil {
			t.Errorf("con expiresAt = %v (%T) la chiave resta bloccata: %v — "+
				"un lease malformato deve valere come scaduto", bad, bad, err)
		}
		if _, err := coll.DeleteOne(ctx, bson.M{"_id": "rotta"}); err != nil {
			t.Fatalf("pulizia: %v", err)
		}
	}
}

// La scadenza la deve calcolare il server: il documento scritto deve portare un istante derivato da
// $$NOW, non uno spedito dal client.
func TestScadenzaCalcolataDalServer(t *testing.T) {
	store, coll := newStore(t)
	ctx := context.Background()

	if err := store.TryAcquire(ctx, "k", "tok", 30*time.Second); err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}

	var doc struct {
		ExpiresAt time.Time `bson:"expiresAt"`
	}
	if err := coll.FindOne(ctx, bson.M{"_id": "k"}).Decode(&doc); err != nil {
		t.Fatalf("lettura del documento: %v", err)
	}
	if delta := time.Until(doc.ExpiresAt); delta < 25*time.Second || delta > 35*time.Second {
		t.Errorf("scadenza fra %v, attesi ~30s", delta)
	}
}

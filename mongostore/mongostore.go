// Package mongostore tiene i lease in una collection MongoDB: è il backend Mongo di go-core-locker.
//
//	corelock.Module(&svc.Lock, corelock.WithBackend(mongostore.Module))
//
// Un documento per chiave: `{_id: <chiave>, owner: <token>, expiresAt: <data>}`.
package mongostore

import (
	"context"
	"time"

	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-app"

	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// errs costruisce gli errori del package con l'ambito della libreria (vedi core.AmbitErrors).
var errs = core.AmbitErrors{Ambit: corelock.Ambit}

// Database è ciò che lo store usa del servizio Mongo.
//
// Non `GetCollection`, che risolve un id dichiarato in `mongo.collections` del config: la
// collection dei lease **non è una collection dell'applicazione** — l'app non la legge mai, e il
// suo nome è un knob di questa libreria (`services.lock.mongo.collection`). Pretenderne la
// dichiarazione anche nel linked service sarebbe una seconda sede per la stessa scelta, e
// romperebbe ogni applicazione che aggiorna senza toccare il proprio config.
type Database interface {
	Db() *mongo.Database
}

// Store è il LeaseStore su MongoDB.
type Store struct {
	svc        Database
	collection string
}

// New costruisce lo store sul servizio Mongo dell'applicazione.
//
// La collection si risolve a ogni operazione e non alla costruzione: il servizio Mongo non è
// connesso quando fx costruisce il grafo, e legarsi lì costringeva a un hook di lifecycle e a un
// errore "il locker non è ancora partito" che nessuno poteva gestire.
func New(svc Database, cfg *corelock.Config) corelock.LeaseStore {
	c := (&corelock.Config{}).WithDefaults()
	if cfg != nil {
		c = cfg.WithDefaults()
	}
	return &Store{svc: svc, collection: c.Mongo.Collection}
}

func (s *Store) coll() (*mongo.Collection, *core.Error) {
	db := s.svc.Db()
	if db == nil {
		return nil, errs.Tech(corelock.CodeSchema).
			WithMessage("il servizio Mongo non è connesso")
	}
	return db.Collection(s.collection), nil
}

// expiryOf è l'istante di scadenza del documento, per il confronto server-side.
//
// Un `expiresAt` assente, nullo o di tipo diverso da data vale **epoch**, cioè scaduto. Senza
// questa clausola un documento malformato non soddisfa il predicato di scadenza, l'upsert ricade
// sull'inserimento e sbatte su duplicate key: quella chiave diventa inacquisibile per sempre, e il
// sintomo è un job che non parte più senza che nulla lo spieghi.
func expiryOf() bson.M {
	return bson.M{"$cond": bson.A{
		bson.M{"$eq": bson.A{bson.M{"$type": "$expiresAt"}, "date"}},
		"$expiresAt",
		time.Unix(0, 0),
	}}
}

// expired: il lease è libero o scaduto, valutato sull'orologio del server (`$$NOW`).
// È un'espressione di aggregazione, non un predicato: vive dentro la pipeline di aggiornamento.
func expired() bson.M {
	return bson.M{"$lte": bson.A{expiryOf(), "$$NOW"}}
}

// stillHeld: il lease è ancora valido, sempre sull'orologio del server.
func stillHeld() bson.M {
	return bson.M{"$expr": bson.M{"$gt": bson.A{expiryOf(), "$$NOW"}}}
}

// setLease scrive il lease incondizionatamente. Serve al rinnovo, dove la condizione sta già nel
// filtro. È una pipeline e non un `$set` semplice perché solo così la scadenza si calcola da
// `$$NOW`, cioè dal tempo del server, invece che da un istante deciso e spedito dal client.
func setLease(token string, ttl time.Duration) mongo.Pipeline {
	return mongo.Pipeline{{{Key: "$set", Value: bson.M{
		"owner":     token,
		"expiresAt": newExpiry(ttl),
	}}}}
}

func newExpiry(ttl time.Duration) bson.M {
	return bson.M{"$add": bson.A{"$$NOW", ttl.Milliseconds()}}
}

// takeIfExpired scrive il lease **solo se** quello presente è libero o scaduto, lasciandolo intatto
// altrimenti.
//
// La condizione sta nella pipeline e non nel filtro perché MongoDB rifiuta `$expr` nel predicato di
// un upsert ("$expr is not allowed in the query predicate for an upsert"), e senza `$expr` non si
// può nominare `$$NOW` — cioè non si può far decidere al server se il lease è scaduto. Spostandola
// qui, il filtro resta il solo `_id` e l'upsert è ammesso.
//
// Con il documento assente l'upsert esegue la pipeline su `{_id: key}`: `expiresAt` manca, quindi
// vale epoch, quindi è scaduto e il lease viene scritto.
func takeIfExpired(token string, ttl time.Duration) mongo.Pipeline {
	cond := expired()
	return mongo.Pipeline{{{Key: "$set", Value: bson.M{
		"owner":     bson.M{"$cond": bson.A{cond, token, "$owner"}},
		"expiresAt": bson.M{"$cond": bson.A{cond, newExpiry(ttl), "$expiresAt"}},
	}}}}
}

func (s *Store) TryAcquire(ctx context.Context, key, token string, ttl time.Duration) error {
	coll, aerr := s.coll()
	if aerr != nil {
		return aerr
	}

	// L'aggiornamento scrive il lease solo se quello presente è scaduto, e restituisce il
	// documento risultante: se il proprietario è il nostro token la chiave è nostra, altrimenti la
	// tiene ancora qualcun altro. Una sola andata e ritorno, e la decisione la prende il server.
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)

	var doc struct {
		Owner string `bson:"owner"`
	}
	err := coll.FindOneAndUpdate(ctx, bson.M{"_id": key}, takeIfExpired(token, ttl), opts).Decode(&doc)
	if err != nil {
		// Due upsert concorrenti sulla stessa chiave inesistente: uno inserisce, l'altro trova la
		// chiave già presente. È contesa, non un guasto.
		if mongo.IsDuplicateKeyError(err) {
			return corelock.ErrNotAcquired
		}
		return errs.Tech(corelock.CodeAcquire).WithCause(err)
	}
	if doc.Owner != token {
		return corelock.ErrNotAcquired
	}
	return nil
}

func (s *Store) Renew(ctx context.Context, key, token string, ttl time.Duration) error {
	coll, aerr := s.coll()
	if aerr != nil {
		return aerr
	}

	// Un lease scaduto è perso anche se nessuno l'ha ancora rubato: rinnovarlo resusciterebbe un
	// lock che la sezione critica non aveva più.
	filter := bson.M{"_id": key, "owner": token, "$and": bson.A{stillHeld()}}

	res, err := coll.UpdateOne(ctx, filter, setLease(token, ttl))
	if err != nil {
		return errs.Tech(corelock.CodeRenew).WithCause(err)
	}
	if res.MatchedCount == 0 {
		return errs.Tech(corelock.CodeRenew).
			WithMessage("lease non più posseduto: " + key).WithCause(corelock.ErrLockLost)
	}
	return nil
}

func (s *Store) Release(ctx context.Context, key, token string) error {
	coll, aerr := s.coll()
	if aerr != nil {
		return aerr
	}

	if _, err := coll.DeleteOne(ctx, bson.M{"_id": key, "owner": token}); err != nil {
		return errs.Tech(corelock.CodeRelease).WithCause(err)
	}
	return nil
}

// EnsureSchema crea l'indice TTL sulla scadenza.
//
// Non serve alla correttezza — il predicato di scadenza la governa già — ma senza di esso i lease
// scaduti restano nella collection finché qualcuno non riusa quella chiave: la collection cresce
// con le chiavi viste una volta sola, e nessuno se ne accorge.
//
// È esplicita come la sua omologa SQL: modificare lo schema resta una scelta dell'applicazione.
func EnsureSchema(ctx context.Context, svc Database, cfg *corelock.Config) *core.Error {
	s, ok := New(svc, cfg).(*Store)
	if !ok {
		return errs.Tech(corelock.CodeSchema).
			WithMessage("store inatteso")
	}
	coll, aerr := s.coll()
	if aerr != nil {
		return aerr
	}

	_, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "expiresAt", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(0).SetName("ix_lock_ttl"),
	})
	if err != nil {
		return errs.Tech(corelock.CodeSchema).WithCause(err)
	}
	return nil
}

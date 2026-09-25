# go-core-locker

Lock distribuito per i microservizi GPA: **un solo motore, N backend di storage**.

Un backend non implementa il locking — implementa tre operazioni su una riga di lease — mentre il
motore tiene per tutti la risoluzione dei default, la generazione del token, il loop dei tentativi e
la semantica di `Release` ed `Extend`.

```
      memstore  ─┐
      sqlstore  ─┼─→ LeaseStore ──→ motore ──→ Locker ──→ scheduler batch
      mongostore ┤                                    └─→ sezioni critiche dell'app
      redisstore ┘
```

**Import:** `github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker` (package `corelock`)

## Wiring

```go
corelock.Module(&svc.Lock,
    corelock.WithBackend(mongostore.Module),   // obbligatoria
    corelock.WithModes(engine.Scheduler))
```

| Option | Effetto |
|---|---|
| `WithBackend(m ModuleFunc)` | **obbligatoria**: dove vivono i lease. Assente ⇒ **panic** al boot |
| `WithModes(modes ...string)` | limita la registrazione ai `core.Mode` indicati |

Il backend si passa **per riferimento diretto**, mai come closure: è ciò che fa sì che il `go.mod`
dell'applicazione elenchi solo quello importato — un'app su Mongo non si porta bun né go-redis.

Il `Locker` esce dal modulo ed è l'unica cosa che ne esce: `Config` e `LeaseStore` restano dentro.
Lo scheduler di `go-core-batch` lo riceve da fx come qualsiasi altra dipendenza; se manca, l'avvio
fallisce con un `missing type`.

## Config — `services.lock`

```yaml
config:
  services:
    lock:
      ttl: 30s               # durata del lease quando la chiamata non la specifica
      retry-delay: 100ms     # attesa fra due tentativi, quando ne è stato chiesto più di uno
      key-prefix: "myapp"    # isola le chiavi dalle altre applicazioni sullo stesso backend
      mongo: { collection: scheduler_locks }
      sql:   { table: scheduler_locks }
```

**`key-prefix` non ha un default**, e il motivo è che inventarne uno cambierebbe le chiavi di chi non
lo usa. Ma senza, due deployment che condividono un Redis o un database si contendono il lock sui
nomi dei propri job — che spesso coincidono, `import`, `purge` — e il sintomo è soltanto un tick che
non parte.

## L'uso

```go
h, err := locker.Acquire(ctx, "import-anagrafiche")
if errors.Is(err, corelock.ErrNotAcquired) {
    return nil // lo sta già facendo un'altra replica
}
if err != nil {
    return err
}
defer func() { _ = h.Release(ctx) }()
```

Senza opzioni: **un tentativo solo, non bloccante**. È il dispatch-dedup su cui si regge lo
scheduler batch, dove la correttezza sta nel claiming sul database e il lock serve solo a non far
eseguire lo stesso tick a N repliche.

Per una sezione critica che va davvero protetta:

```go
h, err := locker.Acquire(ctx, key, corelock.WithWait(30*time.Second, 200*time.Millisecond),
                                   corelock.WithExpiry(time.Minute))
...
if err := h.Extend(ctx); errors.Is(err, corelock.ErrLockLost) {
    return fmt.Errorf("lock perso: la sezione critica sta girando senza protezione")
}
```

`Extend` che ritorna `ErrLockLost` non è un dettaglio da loggare: dice che qualcun altro possiede la
chiave, quindi il lavoro in corso non è più protetto.

## Le regole che valgono per tutti i backend

Non sono convenzioni: sono verificate dalla suite di conformità, che i quattro backend eseguono
identica.

- **La scadenza la valuta il backend**, mai il processo. `LeaseStore` non riceve un `now` proprio
  per impedirlo: gli orologi di due repliche non sono lo stesso orologio, e qualche secondo di
  scarto basta perché due processi ottengano la stessa chiave.
- **Il token è crypto-random**, 16 byte. Serve a impedire che un proprietario liberi o rinnovi il
  lock di un altro, quindi un valore indovinabile non protegge da nulla.
- **`Release` è idempotente** e non segnala il lock già perso.
- **`Release` non libera il lock altrui**: confronta sempre il token.
- **`Extend` su un lease scaduto fallisce** anche se nessuno l'ha ancora rubato: rinnovarlo
  resusciterebbe un lock che la sezione critica non aveva più.
- **Un lease malformato vale come scaduto.** Su Mongo, un `expiresAt` assente o non-data faceva
  fallire il predicato di scadenza e la chiave restava inacquisibile per sempre.

## I backend

| Package | Dove vivono i lease | Dipendenza |
|---|---|---|
| `mongostore` | un documento per chiave | `go-core-mongo` |
| `sqlstore` | una riga per chiave | `go-core-sql` (bun) |
| `redisstore` | una chiave con `PX` | `go-core-redis` (go-redis) |
| `memstore` | una mappa nel processo | — |

**mongo** — `FindOneAndUpdate` con upsert e la condizione di scadenza **dentro la pipeline**: MongoDB
rifiuta `$expr` nel predicato di un upsert, e senza `$expr` non si può nominare `$$NOW`, cioè non si
può far decidere al server. Spostandola nella pipeline il filtro resta il solo `_id`.
`EnsureSchema` crea l'indice TTL.

**sql** — la scadenza è un **intero di millisecondi** e non un timestamp: il confronto fra un
timestamp e `CURRENT_TIMESTAMP` non è portabile — su SQLite bun scrive l'istante in un formato che
non si confronta lessicograficamente — mentre due interi si confrontano allo stesso modo ovunque.
`EnsureSchema` crea la tabella, ed è esplicita: modificare lo schema resta una scelta di chi governa
le migrazioni.

**redis** — `SET NX PX` e due script Lua per rinnovo e rilascio. **Non usa redsync**: con un solo
client `redsync.New(pool)` non esegue Redlock, fa un `SET NX` — la dipendenza non comprava il quorum
che il suo nome suggeriva, e imponeva una semantica sua. Il vero Redlock multi-nodo, se servirà, è un
backend a parte.

**mem** — non è distribuito, e dirlo è il punto: un secondo processo non vede questi lease. È il
riferimento del motore nei test, ed è un `Locker` legittimo per un'app a replica singola.

## La suite di conformità

```go
func TestConformance(t *testing.T) {
    conformance.Run(t, func(t *testing.T) corelock.LeaseStore { return newStore(t) })
}
```

Dodici prove: contesa, chiavi indipendenti, `Release` che libera / è idempotente / non ruba il lock
altrui, scadenza, `Extend` che mantiene e `Extend` su lock perso, retry che riesce e retry esaurito,
context annullato.

Gira **sempre** su `memstore`, su SQLite per `sqlstore` e su miniredis per `redisstore`; per
`mongostore` serve un database:

```bash
go test ./...                                                    # mem, sql, redis
MONGO_URL=mongodb://localhost:27017 go test ./mongostore/...     # mongo
```

Esiste perché i tre backend storici non avevano **alcun** test sulla semantica del lock, e infatti
erano divergenti su scadenza, token e `Release` senza che nulla lo rilevasse. Un backend nuovo la
esegue, e se non la passa non è un backend.

## Scrivere un backend

```go
type LeaseStore interface {
    TryAcquire(ctx context.Context, key, token string, ttl time.Duration) error
    Renew(ctx context.Context, key, token string, ttl time.Duration) error
    Release(ctx context.Context, key, token string) error
}
```

`TryAcquire` ritorna `ErrNotAcquired` — e nient'altro — quando la chiave è tenuta: è contesa, non un
guasto, e il motore la distingue per decidere se riprovare. Il resto della semantica non è affar suo.

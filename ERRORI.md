# Errori di go-core-locker

Ogni errore nato dentro questa libreria porta `Ambit = corelock.Ambit` (`"go-core-locker"`): i
costruttori di `core.*Error()` riempiono `Ambit` con l'`AppName`, cioè con l'applicazione che
*riceve* l'errore, e senza sovrascriverlo un guasto della libreria si presenterebbe come un errore
dell'applicazione.

| Codice | Costante | Origine | Significato |
|---|---|---|---|
| `LOCK-ACQUIRE` | `corelock.CodeAcquire` | i backend | il backend non ha potuto tentare l'acquisizione (guasto, non contesa) |
| `LOCK-RENEW` | `corelock.CodeRenew` | i backend | rinnovo fallito: guasto del backend, oppure lease non più posseduto |
| `LOCK-RELEASE` | `corelock.CodeRelease` | i backend | il rilascio è fallito per un guasto del backend |
| `LOCK-TOKEN` | `corelock.CodeToken` | `lease.go` | la generazione del token di proprietà non è riuscita |
| `LOCK-SCHEMA` | `corelock.CodeSchema` | `EnsureSchema`, risoluzione della collection | creazione di tabella/indice fallita, o collection non configurata |

## Le due sentinelle non sono guasti

`ErrNotAcquired` ed `ErrLockLost` non sono `ApplicationError` e non hanno un codice: sono
**condizioni**, non errori operativi, e si riconoscono con `errors.Is`.

| Sentinella | Quando | Cosa farne |
|---|---|---|
| `corelock.ErrNotAcquired` | la chiave è tenuta da un altro proprietario | saltare la sezione critica e riprovare più tardi. È l'esito normale del dispatch-dedup fra repliche |
| `corelock.ErrLockLost` | `Extend` su un lease scaduto o passato ad altri | la sezione critica sta girando **senza protezione**: interromperla, non proseguire |

`ErrLockLost` viaggia come causa di un `ApplicationError` con codice `LOCK-RENEW`, quindi
`errors.Is(err, corelock.ErrLockLost)` funziona e insieme si conserva l'ambito di chi l'ha prodotto.

## Quello che non è un errore

**`Release` di un lock già perso ritorna `nil`.** Chi rilascia una chiave che non ha più l'ha già
persa, e l'unico sito di chiamata è un `defer` che di fronte a quell'errore non avrebbe nulla da
scegliere. È anche il punto su cui i tre backend storici divergevano: Redis segnalava errore, Mongo
e SQL no.

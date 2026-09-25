package corelock

import (
	"context"
	"time"
)

// LeaseStore è il seam dei backend: l'unica cosa che un backend implementa.
//
// Sono tre operazioni su una riga di lease — chiave, proprietario, scadenza — e nessuna di esse
// decide qualcosa sul locking: i default, il numero di tentativi, l'attesa fra l'uno e l'altro e la
// generazione del token appartengono al motore, che è uno solo per tutti i backend.
//
// # La scadenza la valuta il backend
//
// Nessun metodo riceve un `now`, ed è deliberato: un'implementazione non deve poter confrontare la
// scadenza col proprio orologio. Gli orologi di due repliche non sono lo stesso orologio, e con
// qualche secondo di scarto due processi otterrebbero la stessa chiave — cioè il lock smetterebbe
// di essere un lock. Il confronto va fatto dove il tempo è condiviso: `$$NOW` su Mongo,
// `CURRENT_TIMESTAMP` su SQL, la scadenza nativa `PX` su Redis.
type LeaseStore interface {
	// TryAcquire scrive il lease per key se la chiave è libera o il suo lease è scaduto.
	//
	// Ritorna ErrNotAcquired — e nient'altro — quando la chiave è tenuta da un altro proprietario:
	// è contesa, non un guasto, e il motore la distingue per decidere se riprovare. Un lease
	// malformato (scadenza assente o di tipo inatteso) va trattato come scaduto: altrimenti resta
	// lì per sempre e quella chiave non è più acquisibile da nessuno.
	TryAcquire(ctx context.Context, key, token string, ttl time.Duration) error

	// Renew sposta in avanti la scadenza del lease, ma solo se è ancora di token.
	// Ritorna ErrLockLost se la chiave non esiste più, è scaduta, o è passata ad altri.
	Renew(ctx context.Context, key, token string, ttl time.Duration) error

	// Release rimuove il lease, ma solo se è di token: nessun proprietario deve poter liberare il
	// lock di un altro.
	//
	// È idempotente e non segnala il lock già perso: chi rilascia una chiave che non ha più l'ha
	// già persa, e l'unico sito di chiamata è un defer che non ha alternative da scegliere.
	Release(ctx context.Context, key, token string) error
}

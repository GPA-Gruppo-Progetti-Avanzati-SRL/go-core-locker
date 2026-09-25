package corelock

// Ambit è l'ambito degli errori nati dentro questa libreria. Va messo con WithAmbit su ogni
// ApplicationError costruito qui: i costruttori base riempiono Ambit con l'AppName, cioè con
// l'applicazione che riceve l'errore, e senza sovrascriverlo un guasto della libreria si
// presenterebbe come un errore dell'applicazione.
const Ambit = "go-core-locker"

// Codici degli errori della libreria. La tabella completa è in ERRORI.md.
const (
	// CodeAcquire — il backend non ha potuto tentare l'acquisizione.
	CodeAcquire = "LOCK-ACQUIRE"
	// CodeRenew — il rinnovo del lease è fallito per un guasto del backend (non per lock perso,
	// che è ErrLockLost).
	CodeRenew = "LOCK-RENEW"
	// CodeRelease — il rilascio è fallito per un guasto del backend.
	CodeRelease = "LOCK-RELEASE"
	// CodeToken — la generazione del token non è riuscita.
	CodeToken = "LOCK-TOKEN"
	// CodeSchema — la creazione di collection/tabella o dei loro indici è fallita.
	CodeSchema = "LOCK-SCHEMA"
)

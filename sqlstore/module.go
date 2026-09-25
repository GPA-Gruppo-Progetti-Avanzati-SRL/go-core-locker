package sqlstore

import (
	core "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-app"
	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
)

// Module registra il backend SQL. Si passa a corelock.WithBackend per riferimento diretto:
//
//	corelock.Module(&svc.Lock, corelock.WithBackend(sqlstore.Module))
//
// Richiede il *coresql.Service dell'applicazione, cioè che coresql.Module sia wirato. La tabella
// resta a carico dell'app: EnsureSchema o una migrazione.
func Module(modes ...string) {
	core.ProvideAs[corelock.LeaseStore](New, modes...)
}

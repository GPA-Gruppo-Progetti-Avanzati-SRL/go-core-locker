package mongostore

import (
	core "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-app"
	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
	coremongo "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-mongo"
)

// Module registra il backend Mongo. Si passa a corelock.WithBackend per riferimento diretto:
//
//	corelock.Module(&svc.Lock, corelock.WithBackend(mongostore.Module))
//
// Richiede il *coremongo.Service dell'applicazione, cioè che coremongo.Module sia wirato.
func Module(modes ...string) {
	core.ProvideAs[corelock.LeaseStore](newStore, modes...)
}

func newStore(svc *coremongo.Service, cfg *corelock.Config) corelock.LeaseStore {
	return New(svc, cfg)
}

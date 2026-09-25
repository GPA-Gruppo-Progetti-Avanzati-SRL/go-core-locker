package redisstore

import (
	core "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-app"
	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
)

// Module registra il backend Redis. Si passa a corelock.WithBackend per riferimento diretto:
//
//	corelock.Module(&svc.Lock, corelock.WithBackend(redisstore.Module))
//
// Richiede il *goredis.Client dell'applicazione, cioè che redis.Module di go-core-redis sia wirato
// prima.
func Module(modes ...string) {
	core.ProvideAs[corelock.LeaseStore](New, modes...)
}

package corelock

import (
	core "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-app"
)

// ModuleFunc è la forma con cui si passa un backend: una registrazione fx che riceve solo i modes.
//
// Si passa per riferimento diretto — corelock.WithBackend(mongostore.Module) — e mai come closure:
// è ciò che fa sì che il go.mod dell'applicazione elenchi soltanto il backend che ha davvero
// importato, invece di tutti quelli che la libreria conosce.
type ModuleFunc func(modes ...string)

// Option configura Module.
type Option func(*options)

type options struct {
	modes   []string
	backend ModuleFunc
}

// WithModes limita la registrazione ai core.Mode indicati (nessuno = sempre).
func WithModes(modes ...string) Option {
	return func(o *options) { o.modes = modes }
}

// WithBackend sceglie dove vivono i lease. È obbligatoria.
//
//	corelock.WithBackend(mongostore.Module)   // go-core-locker/mongostore
//	corelock.WithBackend(sqlstore.Module)     // go-core-locker/sqlstore
//	corelock.WithBackend(redisstore.Module)   // go-core-locker/redisstore
//	corelock.WithBackend(memstore.Module)     // in-process: una replica sola
func WithBackend(m ModuleFunc) Option {
	return func(o *options) { o.backend = m }
}

// Module wira il lock distribuito: è l'unico entry-point della libreria.
//
//	corelock.Module(&svc.Lock,
//	    corelock.WithBackend(mongostore.Module),
//	    corelock.WithModes(engine.Scheduler))
//
// Espone al grafo il Locker e nient'altro: Config e LeaseStore restano dentro il modulo.
func Module(cfg *Config, opts ...Option) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.backend == nil {
		panic("corelock.Module: WithBackend è obbligatoria — senza un backend non esiste un lock " +
			"condiviso fra le repliche. Scegliere fra corelock.WithBackend(mongostore.Module), " +
			"(sqlstore.Module), (redisstore.Module) o (memstore.Module) per una replica sola")
	}
	cfg.WithDefaults()

	core.Module("lock", func() {
		core.Supply(cfg, o.modes...)

		// Il LeaseStore è lo storage del motore, non un servizio dell'applicazione: resta privato
		// al modulo. Sta qui dentro e non fuori perché il motore dipende da lui — un costruttore
		// registrato a root non vedrebbe un provider privato di un modulo discendente.
		core.Private(func() { o.backend(o.modes...) })

		core.ProvideAs[Locker](newLocker, o.modes...)
	})
}

func newLocker(store LeaseStore, cfg *Config) Locker { return NewLocker(store, cfg) }

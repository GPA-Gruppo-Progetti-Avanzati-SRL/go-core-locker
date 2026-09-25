package corelock

import "time"

// Config è la sezione `services.lock` della configurazione.
//
// Prima non esisteva: TTL, attesa fra i tentativi e il nome di collection e tabella erano costanti
// nel codice dei backend, e il prefisso delle chiavi non c'era affatto.
//
// Le sezioni dei backend stanno qui e non nei rispettivi sottopackage perché il package root non
// deve importarli — è la condizione per cui un'applicazione che sceglie un backend non si porta
// dietro i driver degli altri.
type Config struct {
	// TTL è la durata del lease quando la chiamata non la specifica (AcquireOption WithExpiry).
	TTL time.Duration `yaml:"ttl" mapstructure:"ttl" json:"ttl"`
	// RetryDelay è l'attesa fra due tentativi, quando ne è stato chiesto più di uno.
	RetryDelay time.Duration `yaml:"retry-delay" mapstructure:"retry-delay" json:"retry-delay"`
	// KeyPrefix isola le chiavi di questa applicazione da quelle delle altre che condividono lo
	// stesso backend. Senza, due deployment si contendono il lock sui nomi dei propri job — che
	// spesso coincidono — e il sintomo è soltanto un tick che non parte.
	KeyPrefix string `yaml:"key-prefix" mapstructure:"key-prefix" json:"key-prefix"`

	Mongo MongoConfig `yaml:"mongo" mapstructure:"mongo" json:"mongo"`
	Sql   SQLConfig   `yaml:"sql"   mapstructure:"sql"   json:"sql"`
}

// MongoConfig configura il backend mongo: la collection che ospita i lease.
type MongoConfig struct {
	Collection string `yaml:"collection" mapstructure:"collection" json:"collection"`
}

// SQLConfig configura il backend sql: la tabella che ospita i lease.
type SQLConfig struct {
	Table string `yaml:"table" mapstructure:"table" json:"table"`
}

// DefaultLeaseTable è il nome di collection e tabella quando la config non lo dice.
const DefaultLeaseTable = "scheduler_locks"

// WithDefaults riempie i campi non valorizzati. È idempotente.
func (c *Config) WithDefaults() *Config {
	if c.TTL <= 0 {
		c.TTL = DefaultTTL
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = DefaultRetryDelay
	}
	if c.Mongo.Collection == "" {
		c.Mongo.Collection = DefaultLeaseTable
	}
	if c.Sql.Table == "" {
		c.Sql.Table = DefaultLeaseTable
	}
	return c
}

// withDefaults risolve la config accettando il nil, perché NewLocker è usata anche dai test di
// conformità, che non hanno una configurazione da passare.
func (c *Config) withDefaults() Config {
	if c == nil {
		return *(&Config{}).WithDefaults()
	}
	return *c.WithDefaults()
}

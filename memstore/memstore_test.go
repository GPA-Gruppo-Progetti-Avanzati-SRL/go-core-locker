package memstore_test

import (
	"testing"

	corelock "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker"
	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/conformance"
	"github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/memstore"
)

// Il backend in-process esegue la suite completa, e la esegue sempre: è il riferimento del motore,
// quindi una regressione nel motore si vede qui anche dove non c'è infrastruttura.
func TestConformance(t *testing.T) {
	conformance.Run(t, func(*testing.T) corelock.LeaseStore { return memstore.New() })
}

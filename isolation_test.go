package corelock_test

import (
	"os/exec"
	"strings"
	"testing"
)

// Ogni backend è un package che l'app sceglie con un import, e quell'import deve portare il SUO
// database e nessun altro (vedi il gemello in go-core-batch/store). Si legge dal grafo dei
// package, cioè da ciò che il compilatore costruisce davvero.
func TestBackend_NonPortanoGliAltriDatabase(t *testing.T) {
	const m = "github.com/GPA-Gruppo-Progetti-Avanzati-SRL/go-core-locker/"
	cases := []struct {
		pkg       string
		forbidden []string
	}{
		{"sqlstore", []string{m + "oraclestore", "mongo-driver", "go-redis"}},
		{"oraclestore", []string{m + "sqlstore", "mongo-driver", "go-redis"}},
		{"mongostore", []string{"uptrace/bun", "go-redis"}},
		{"redisstore", []string{"uptrace/bun", "mongo-driver"}},
	}
	for _, c := range cases {
		t.Run(c.pkg, func(t *testing.T) {
			out, err := exec.Command("go", "list", "-deps", m+c.pkg).Output()
			if err != nil {
				t.Fatalf("go list -deps %s: %v", c.pkg, err)
			}
			deps := strings.Fields(string(out))
			if len(deps) == 0 {
				t.Fatalf("go list -deps %s: nessun package, la verifica non verificherebbe nulla", c.pkg)
			}
			for _, dep := range deps {
				for _, f := range c.forbidden {
					if strings.Contains(dep, f) {
						t.Errorf("%s compila %s", c.pkg, dep)
					}
				}
			}
		})
	}
}

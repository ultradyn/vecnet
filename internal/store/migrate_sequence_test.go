package store

import (
	"fmt"
	"strings"
	"testing"
)

func TestMigrationNumbersAreUniqueAndContiguous(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("no embedded migrations")
	}
	for i, migration := range migrations {
		want := fmt.Sprintf("%04d_", i+1)
		if !strings.HasPrefix(migration.name, want) {
			t.Errorf("migration %d = %q, want prefix %q", i+1, migration.name, want)
		}
	}
}

package migrations

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeBackend struct {
	applied  []AppliedMigration
	executed []string
	recorded []Migration
	failAt   int
}

func (b *fakeBackend) Prepare(context.Context) error                       { return nil }
func (b *fakeBackend) Applied(context.Context) ([]AppliedMigration, error) { return b.applied, nil }
func (b *fakeBackend) Execute(_ context.Context, statement string) error {
	b.executed = append(b.executed, statement)
	if b.failAt > 0 && len(b.executed) == b.failAt {
		return errors.New("failed")
	}
	return nil
}
func (b *fakeBackend) Record(_ context.Context, migration Migration) error {
	b.recorded = append(b.recorded, migration)
	return nil
}

func TestRunnerRecordsEveryMigrationAfterStatements(t *testing.T) {
	backend := &fakeBackend{}
	if err := Run(context.Background(), backend); err != nil {
		t.Fatal(err)
	}
	migrations, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(backend.recorded) != len(migrations) || len(backend.executed) < len(migrations) {
		t.Fatalf("recorded %d, executed %d, migrations %d", len(backend.recorded), len(backend.executed), len(migrations))
	}
}

func TestRunnerRejectsChecksumDriftBeforeExecuting(t *testing.T) {
	migrations, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{applied: []AppliedMigration{{Version: migrations[0].Version, Name: migrations[0].Name, Checksum: "different"}}}
	if err := Run(context.Background(), backend); err == nil {
		t.Fatal("checksum drift was accepted")
	}
	if len(backend.executed) != 0 {
		t.Fatal("runner executed SQL after checksum drift")
	}
}

func TestRunnerRejectsUnknownNewerLedgerVersion(t *testing.T) {
	backend := &fakeBackend{applied: []AppliedMigration{{Version: 999, Name: "999_future.sql", Checksum: "future"}}}
	if err := Run(context.Background(), backend); err == nil {
		t.Fatal("old runner accepted a newer unknown migration")
	}
	if len(backend.executed) != 0 {
		t.Fatal("runner executed SQL after detecting a newer ledger version")
	}
}

func TestSplitStatementsPreservesQuotedSemicolon(t *testing.T) {
	statements, err := splitStatements("SELECT ';'; -- ignored\nSELECT 2;")
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 2 || statements[0] != "SELECT ';'" || statements[1] != "SELECT 2" {
		t.Fatalf("statements = %#v", statements)
	}
}

func TestFrozenV1MigrationChecksums(t *testing.T) {
	want := map[uint32]string{
		1: "c3d2ea6a643a0d6c3c902c3ecc43ddceacb44de440a3c00e8509cf2e3beedb3e",
		2: "bdf7380d9c1d60f8a4e6e3b77ff519d1087cf955f2be533de758a932a0d11986",
		3: "2fcb5793101ce7a7137745cb669a7c63fe35da38f80144121c0e8fa5f6f2c840",
	}
	migrations, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if checksum, ok := want[migration.Version]; ok && migration.Checksum != checksum {
			t.Fatalf("frozen V1 migration %03d changed: got %s", migration.Version, migration.Checksum)
		}
	}
}

func TestMultiProductMigrationsAreEmbedded(t *testing.T) {
	migrations, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if latest := migrations[len(migrations)-1]; latest.Version != 12 || latest.Name != "012_v2_datalens_multi_product_views.sql" {
		t.Fatalf("latest migration = %03d %s", latest.Version, latest.Name)
	}
	var productSQL, datalensSQL string
	for _, migration := range migrations {
		switch migration.Version {
		case 11:
			productSQL = strings.Join(migration.Statements, "\n")
		case 12:
			datalensSQL = strings.Join(migration.Statements, "\n")
		}
	}
	if !strings.Contains(productSQL, "product_events_v2") || !strings.Contains(productSQL, "datalens_product_v2") {
		t.Fatal("product stream migration is incomplete")
	}
	if !strings.Contains(datalensSQL, "datalens_common_v3") || !strings.Contains(datalensSQL, "datalens_game_sessions_v3") {
		t.Fatal("multi-product DataLens migration is incomplete")
	}
}

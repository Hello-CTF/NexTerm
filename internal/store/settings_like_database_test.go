package store_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestSettingListPrefixEscapesLikePatternDatabase(t *testing.T) {
	fixture := dbtest.NewFixture(t)
	assertSettingListPrefixLiteralMatch(t, fixture.OpenStore(t))
	if fixture.Backend == store.BackendMySQL || fixture.Backend == store.BackendMariaDB {
		t.Run("no_backslash_escapes", func(t *testing.T) {
			isolated := dbtest.NewFixture(t)
			assertSettingListPrefixLiteralMatch(t, openExactLikeNoBackslashStore(t, isolated))
		})
	}
}

func assertSettingListPrefixLiteralMatch(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	tests := []struct {
		name            string
		prefix          string
		lookalikePrefix string
	}{
		{name: "escape", prefix: "contract.bang!.", lookalikePrefix: "contract.bangX."},
		{name: "percent", prefix: "contract.percent%.", lookalikePrefix: "contract.percentX."},
		{name: "underscore", prefix: "contract.underscore_.", lookalikePrefix: "contract.underscoreX."},
		{name: "backslash", prefix: `contract.backslash\.`, lookalikePrefix: "contract.backslash."},
		{name: "chinese", prefix: "contract.中文.", lookalikePrefix: "contract.其他."},
		{name: "ascii_case", prefix: "contract.Case.", lookalikePrefix: "contract.case."},
		{name: "combined", prefix: `contract.combined!%_\.`, lookalikePrefix: "contract.combinedWXYZ."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matchingKey := test.prefix + "theme"
			if err := db.SettingSet(ctx, matchingKey, "literal"); err != nil {
				t.Fatal(err)
			}
			if err := db.SettingSet(ctx, test.lookalikePrefix+"theme", "lookalike"); err != nil {
				t.Fatal(err)
			}
			got, err := db.SettingListPrefix(ctx, test.prefix)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{matchingKey: "literal"}
			if !maps.Equal(got, want) {
				t.Fatalf("SettingListPrefix(%q) = %v, want %v", test.prefix, got, want)
			}
		})
	}
}

func TestSettingContainsValueEscapesLikePatternDatabase(t *testing.T) {
	fixture := dbtest.NewFixture(t)
	assertSettingContainsValueLiteralMatch(t, fixture.OpenStore(t))
	if fixture.Backend == store.BackendMySQL || fixture.Backend == store.BackendMariaDB {
		t.Run("no_backslash_escapes", func(t *testing.T) {
			isolated := dbtest.NewFixture(t)
			assertSettingContainsValueLiteralMatch(t, openExactLikeNoBackslashStore(t, isolated))
		})
	}
}

func assertSettingContainsValueLiteralMatch(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	tests := []struct {
		name      string
		substring string
		lookalike string
	}{
		{name: "escape", substring: "bang!value", lookalike: "bangXvalue"},
		{name: "percent", substring: "percent%value", lookalike: "percentXvalue"},
		{name: "underscore", substring: "under_score", lookalike: "underXscore"},
		{name: "backslash", substring: `back\slash`, lookalike: "backslash"},
		{name: "chinese", substring: "中文值", lookalike: "其他值"},
		{name: "ascii_case", substring: "SecretToken", lookalike: "secrettoken"},
		{name: "combined", substring: `组合!%_\值`, lookalike: "组合WXYZ值"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := db.SettingSet(ctx, "contains."+test.name+".lookalike", "before "+test.lookalike+" after"); err != nil {
				t.Fatal(err)
			}
			got, err := db.SettingContainsValue(ctx, test.substring)
			if err != nil {
				t.Fatal(err)
			}
			if got {
				t.Fatalf("SettingContainsValue(%q) matched lookalike value %q", test.substring, test.lookalike)
			}
			if err := db.SettingSet(ctx, "contains."+test.name+".literal", "before "+test.substring+" after"); err != nil {
				t.Fatal(err)
			}
			got, err = db.SettingContainsValue(ctx, test.substring)
			if err != nil {
				t.Fatal(err)
			}
			if !got {
				t.Fatalf("SettingContainsValue(%q) did not match its literal value", test.substring)
			}
		})
	}
}

func openExactLikeNoBackslashStore(t *testing.T, fixture *dbtest.Fixture) *store.Store {
	t.Helper()
	config, err := mysqldriver.ParseDSN(fixture.DSN)
	if err != nil {
		t.Fatal(err)
	}
	if config.Params == nil {
		config.Params = map[string]string{}
	}
	config.Params["sql_mode"] = "NO_BACKSLASH_ESCAPES"
	db, err := store.OpenBackend(t.Context(), store.BackendConfig{Backend: fixture.Backend, DSN: config.FormatDSN()}, store.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var sqlMode string
	if err := db.DB().QueryRowContext(t.Context(), "SELECT @@SESSION.sql_mode").Scan(&sqlMode); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlMode, "NO_BACKSLASH_ESCAPES") {
		t.Fatalf("session sql_mode = %q, want NO_BACKSLASH_ESCAPES", sqlMode)
	}
	return db
}

package guard

import "testing"

func TestSQLReadOnlyClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		sql  string
		risk Risk
	}{
		{"SELECT * FROM users", Safe},
		{"SELECT COUNT(*), VERSION(), INSERT('abcd', 1, 0, 'x'), TRUNCATE(1.23, 1)", Safe},
		{"SELECT mutating_stored_function()", NeedsConfirm},
		{"SELECT SLEEP(1)", NeedsConfirm},
		{"WITH x AS (SELECT 1) SELECT * FROM x", Safe},
		{"SHOW TABLES", Safe},
		{"DESCRIBE users", Safe},
		{"EXPLAIN SELECT * FROM users", Safe},
		{"SELECT ';DROP DATABASE prod;--'", Safe},
		{"SELECT '/* not a comment */'", Safe},
		{"SELECT 1; DROP TABLE users", NeedsConfirm},
		{"SELECT 1; DROP DATABASE prod", Forbidden},
		{"SELECT * INTO OUTFILE '/tmp/x' FROM users", NeedsConfirm},
		{"SELECT * FROM users FOR UPDATE", NeedsConfirm},
		{"EXPLAIN ANALYZE DELETE FROM users", NeedsConfirm},
		{"USE other_database", NeedsConfirm},
		{"SET sql_safe_updates=0", NeedsConfirm},
		{"TRUNCATE /*comment*/ TABLE users", Danger},
		{"DROP/*comment*/DATABASE prod", Forbidden},
		{"/*!50000 DROP DATABASE prod */;", Forbidden},
		{"/* ordinary comment only */", NeedsConfirm},
		{"drop\nschema\nprod", Forbidden},
		{"SELECT 'unterminated", NeedsConfirm},
	}
	for _, test := range tests {
		t.Run(test.sql, func(t *testing.T) {
			if got := ClassifySQL(test.sql, nil); got.Risk != test.risk {
				t.Fatalf("ClassifySQL(%q) = %v, want %s", test.sql, got, test.risk)
			}
		})
	}
}

func TestSQLCustomDangerRules(t *testing.T) {
	t.Parallel()
	if got := ClassifySQL("SELECT * FROM forbidden_table", []string{"forbidden_table"}); got.Risk != Danger {
		t.Fatalf("got %v", got)
	}
}

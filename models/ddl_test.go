package models

import "testing"

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			"dictionary source",
			"SOURCE(CLICKHOUSE(HOST 'ch' USER 'reader' PASSWORD 's3cr3t' DB 'db'))",
			"SOURCE(CLICKHOUSE(HOST 'ch' USER 'reader' PASSWORD '[HIDDEN]' DB 'db'))",
		},
		{
			"key = value, lower case",
			"SETTINGS password = 'p@ss', user = 'u'",
			"SETTINGS password = '[HIDDEN]', user = 'u'",
		},
		{
			"escaped quote inside the secret",
			`PASSWORD 'it\'s'`,
			"PASSWORD '[HIDDEN]'",
		},
		{
			"already masked by ClickHouse",
			"PASSWORD '[HIDDEN]'",
			"PASSWORD '[HIDDEN]'",
		},
		{
			"a column that merely mentions password",
			"CREATE TABLE db.users (`encryptedPassword` String, `password_hash` String) ENGINE = MergeTree ORDER BY tuple()",
			"CREATE TABLE db.users (`encryptedPassword` String, `password_hash` String) ENGINE = MergeTree ORDER BY tuple()",
		},
	}
	for _, tc := range cases {
		if got := redactSecrets(tc.in); got != tc.want {
			t.Errorf("%s: redactSecrets(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

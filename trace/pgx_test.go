package trace

import "testing"

func TestQueryOperation(t *testing.T) {
	tests := map[string]string{
		"":                             "unknown",
		"   ":                          "unknown",
		"select * from users":          "SELECT",
		"\n\tinsert into users values": "INSERT",
		"with rows as (select 1)":      "WITH",
	}

	for sql, want := range tests {
		t.Run(want, func(t *testing.T) {
			if got := queryOperation(sql); got != want {
				t.Fatalf("queryOperation(%q) = %q, want %q", sql, got, want)
			}
		})
	}
}

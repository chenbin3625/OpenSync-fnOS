package mapper

import "testing"

func TestStripOrderByOnlyRemovesTopLevelClause(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "no order by",
			sql:  "SELECT * FROM job WHERE id=?",
			want: "SELECT * FROM job WHERE id=?",
		},
		{
			name: "top level clause is removed",
			sql:  "SELECT * FROM job ORDER BY createTime DESC",
			want: "SELECT * FROM job",
		},
		{
			name: "subquery clause is preserved",
			sql:  "SELECT * FROM (SELECT id FROM job ORDER BY id) AS t",
			want: "SELECT * FROM (SELECT id FROM job ORDER BY id) AS t",
		},
		{
			name: "subquery preserved while top level is cut",
			sql:  "SELECT * FROM (SELECT id FROM job ORDER BY id) AS t ORDER BY t.id DESC",
			want: "SELECT * FROM (SELECT id FROM job ORDER BY id) AS t",
		},
		{
			name: "clause inside a string literal is not a clause",
			sql:  "SELECT * FROM job WHERE remark=' ORDER BY x'",
			want: "SELECT * FROM job WHERE remark=' ORDER BY x'",
		},
		{
			name: "escaped quote does not end the literal early",
			sql:  "SELECT * FROM job WHERE remark=''' ORDER BY x' ORDER BY id",
			want: "SELECT * FROM job WHERE remark=''' ORDER BY x'",
		},
		{
			name: "lowercase clause",
			sql:  "SELECT * FROM job order by id",
			want: "SELECT * FROM job",
		},
		{
			name: "last top level clause wins",
			sql:  "SELECT * FROM (SELECT a FROM x ORDER BY a) u ORDER BY b ORDER BY c",
			want: "SELECT * FROM (SELECT a FROM x ORDER BY a) u ORDER BY b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripOrderBy(tc.sql); got != tc.want {
				t.Fatalf("stripOrderBy(%q)\n got  %q\n want %q", tc.sql, got, tc.want)
			}
		})
	}
}

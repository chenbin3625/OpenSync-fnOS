package mapper

import "testing"

// "?statusIn=2,7" arrives as []string{"2,7"}. Before the split it became
// status IN (0) — a silent "waiting only" filter instead of the requested set.
func TestParseStatusListSplitsCommaSeparatedEntries(t *testing.T) {
	cases := []struct {
		name  string
		value interface{}
		want  []int
	}{
		{"single csv string in slice", []string{"2,7"}, []int{2, 7}},
		{"multiple slice entries", []string{"2", "7"}, []int{2, 7}},
		{"mixed slice entries", []string{"2,3", "7"}, []int{2, 3, 7}},
		{"bare csv string", "2,3,4", []int{2, 3, 4}},
		{"blanks skipped", []string{"2,,7", ""}, []int{2, 7}},
		{"unparseable dropped not coerced to zero", []string{"abc"}, []int{}},
		{"partially unparseable", []string{"2,abc,7"}, []int{2, 7}},
		{"spaces tolerated", []string{" 2 , 7 "}, []int{2, 7}},
		{"int slice passthrough", []int{4, 5}, []int{4, 5}},
		{"nil", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseStatusList(tc.value)
			if len(got) != len(tc.want) {
				t.Fatalf("parseStatusList(%#v) = %#v, want %#v", tc.value, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("parseStatusList(%#v)[%d] = %d, want %d", tc.value, i, got[i], tc.want[i])
				}
			}
		})
	}
}

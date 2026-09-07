package mcpcompat

import "testing"

func TestEncodeSentinelValueOnlyEncodesAmbiguousBoundaries(t *testing.T) {
	for _, test := range []struct {
		name string
		in   string
		want string
	}{
		{name: "ascii", in: "Madrid centro", want: "Madrid centro"},
		{name: "leading space", in: " Madrid", want: "=?base64?IE1hZHJpZA==?="},
		{name: "trailing space", in: "Madrid ", want: "=?base64?TWFkcmlkIA==?="},
		{name: "unicode", in: "Madrid ñ", want: "=?base64?TWFkcmlkIMOx?="},
		{name: "control", in: "line\nfeed", want: "=?base64?bGluZQpmZWVk?="},
		{name: "ambiguous sentinel", in: "=?base64?plain?=", want: "=?base64?PT9iYXNlNjQ/cGxhaW4/PQ==?="},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := EncodeSentinelValue(test.in); got != test.want {
				t.Fatalf("EncodeSentinelValue(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

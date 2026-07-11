package linearhook

import "testing"

func TestFactoryAuthored(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "standalone signature", body: "Done.\n\n🐘", want: true},
		{name: "marker signature", body: "Done.\n\n🐘 `codex-do:ENG-29:plan-gate:r1`", want: true},
		{name: "trailing blanks and CRLF", body: "Done.\r\n\r\n🐘 `codex-do:ENG-29:implementation-summary:r1`  \r\n\r\n", want: true},
		{name: "emoji in prose", body: "The 🐘 is part of this sentence."},
		{name: "marker in prose", body: "Discuss codex-do:ENG-29:plan-gate:r1 here."},
		{name: "malformed marker", body: "Done.\n\n🐘 `codex-do:not-an-issue:plan-gate:r1`"},
		{name: "prose after signature", body: "🐘\nThis is a human follow-up."},
		{name: "empty", body: " \n\t"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := FactoryAuthored(test.body); got != test.want {
				t.Fatalf("FactoryAuthored() = %t, want %t", got, test.want)
			}
		})
	}
}

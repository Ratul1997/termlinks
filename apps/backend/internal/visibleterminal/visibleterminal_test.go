package visibleterminal

import "testing"

func TestShellQuoteCannotInjectCommands(t *testing.T) {
	got := shellQuote(`/Applications/Term Links/it's; touch /tmp/nope`)
	want := `'` + `/Applications/Term Links/it` + `'"'"'` + `s; touch /tmp/nope` + `'`
	if got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

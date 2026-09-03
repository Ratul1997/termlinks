package visibleterminal

import "testing"

func TestShellQuoteCannotInjectCommands(t *testing.T) {
	got := shellQuote(`/Applications/Term Links/it's; touch /tmp/nope`)
	want := `'` + `/Applications/Term Links/it` + `'"'"'` + `s; touch /tmp/nope` + `'`
	if got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func TestDarwinAttachmentEndsDedicatedShellCleanly(t *testing.T) {
	got := darwinAttachCommand("/Applications/Term Links/termlinks", "0123456789abcdef0123456789abcdef")
	want := `'/Applications/Term Links/termlinks' attach '0123456789abcdef0123456789abcdef'; exit 0`
	if got != want {
		t.Fatalf("darwinAttachCommand() = %q, want %q", got, want)
	}
}

package host

import "testing"

func TestOpenRejectsEmptyDistro(t *testing.T) {
	if err := OpenFolder(""); err == nil {
		t.Fatal("expected error")
	}
	if err := OpenTerminal(""); err == nil {
		t.Fatal("expected error")
	}
}

package ansi

import "testing"

func TestPalette(t *testing.T) {
	if got := (Palette{}).Red("x"); got != "x" {
		t.Errorf("off palette coloured: %q", got)
	}
	p := On()
	if got, want := p.Green("ok"), "\033[32mok\033[0m"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	// An inner colour does not end the outer one.
	if got, want := p.Red("a "+p.Yellow("b")+" c"), "\033[31ma \033[33mb\033[0m\033[31m c\033[0m"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

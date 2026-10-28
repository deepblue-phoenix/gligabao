// Package ansi colours the terminal output of the eval runners: green for passes, red for failures, yellow for names and labels.
package ansi

import (
	"os"
	"strings"
)

// Palette wraps strings in ANSI colours. The zero value (and Palette{} from New on a non-terminal) returns the strings unchanged.
type Palette struct{ on bool }

// New returns a palette that colours only if f is a terminal, NO_COLOR is unset (or FORCE_COLOR is set).
func New(f *os.File) Palette {
	if os.Getenv("NO_COLOR") != "" {
		return Palette{}
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return Palette{on: true}
	}
	st, err := f.Stat()
	return Palette{on: err == nil && st.Mode()&os.ModeCharDevice != 0}
}

// On makes a palette that always colours (for tests).
func On() Palette { return Palette{on: true} }

const (
	reset  = "\033[0m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	dim    = "\033[2m"
)

func (p Palette) wrap(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	// Nested colours: after an inner reset the outer colour continues.
	return code + strings.ReplaceAll(s, reset, reset+code) + reset
}

// Red colours s red (failures).
func (p Palette) Red(s string) string { return p.wrap(red, s) }

// Green colours s green (successes).
func (p Palette) Green(s string) string { return p.wrap(green, s) }

// Yellow colours s yellow (eval names, labels).
func (p Palette) Yellow(s string) string { return p.wrap(yellow, s) }

// Dim dims s.
func (p Palette) Dim(s string) string { return p.wrap(dim, s) }

// Verdict colours s green if ok, else red.
func (p Palette) Verdict(ok bool, s string) string {
	if ok {
		return p.Green(s)
	}
	return p.Red(s)
}

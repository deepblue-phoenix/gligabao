package agent_defs

import (
	"fmt"
	"strconv"
	"strings"
)

// semver is x.y.z; parts says how many of the three a constraint named ("1.2" has parts 2).
type semver struct {
	n     [3]int
	parts int
}

func parseSemver(s string) (semver, error) {
	var v semver
	s = strings.TrimSpace(s)
	fields := strings.Split(s, ".")
	if s == "" || len(fields) > 3 {
		return v, fmt.Errorf("%q is not a version like 1.2.3", s)
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 || (len(f) > 1 && f[0] == '0') || n > 1<<30 {
			return v, fmt.Errorf("%q is not a version like 1.2.3", s)
		}
		v.n[i] = n
	}
	v.parts = len(fields)
	return v, nil
}

// parseVersion reads a full version (three numbers) of a definition.
func parseVersion(s string) (semver, error) {
	v, err := parseSemver(s)
	if err == nil && v.parts != 3 {
		err = fmt.Errorf("%q is not a version like 1.2.3", s)
	}
	return v, err
}

func (a semver) cmp(b semver) int {
	for i := range a.n {
		switch {
		case a.n[i] < b.n[i]:
			return -1
		case a.n[i] > b.n[i]:
			return 1
		}
	}
	return 0
}

// checkConstraint validates a version constraint: comma-separated terms, each an optional operator (>=, <=, >, <, =, ^) and a version of
// one to three numbers. A bare or "=" version matches by its parts ("1.2" is any 1.2.x); "^1.2" is at least 1.2 and below 2.0.
func checkConstraint(c string) error {
	_, err := parseConstraint(c)
	return err
}

type term struct {
	op string
	v  semver
}

func parseConstraint(c string) ([]term, error) {
	var terms []term
	for _, part := range strings.Split(c, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("version constraint %q has an empty term", c)
		}
		op := ""
		for _, o := range []string{">=", "<=", "==", ">", "<", "=", "^"} {
			if strings.HasPrefix(part, o) {
				op = o
				break
			}
		}
		v, err := parseSemver(strings.TrimSpace(strings.TrimPrefix(part, op)))
		if err != nil {
			return nil, fmt.Errorf("version constraint %q: %w",
				c,
				err)
		}
		if op == "==" {
			op = "="
		}
		terms = append(terms, term{op, v})
	}
	return terms, nil
}

// satisfies reports whether the version v meets the constraint c ("" is always met).
func satisfies(version,
	c string) (bool, error) {
	if strings.TrimSpace(c) == "" {
		return true, nil
	}
	v, err := parseVersion(version)
	if err != nil {
		return false, err
	}
	terms, err := parseConstraint(c)
	if err != nil {
		return false, err
	}
	for _, t := range terms {
		var ok bool
		switch t.op {
		case ">=":
			ok = v.cmp(t.v) >= 0
		case "<=":
			ok = v.cmp(t.v) <= 0
		case ">":
			ok = v.cmp(t.v) > 0
		case "<":
			ok = v.cmp(t.v) < 0
		case "^":
			upper := semver{
				n:     [3]int{t.v.n[0] + 1, 0, 0},
				parts: 3,
			}
			ok = v.cmp(t.v) >= 0 && v.cmp(upper) < 0
		default: // "" and "=": the parts that were named
			ok = true
			for i := 0; i < t.v.parts; i++ {
				ok = ok && v.n[i] == t.v.n[i]
			}
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

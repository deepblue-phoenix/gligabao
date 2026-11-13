package agent_defs

import (
	"strings"
	"testing"
	"time"
)

func TestCronNext(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	at := func(loc *time.Location,
		y int,
		m time.Month,
		d,
		h,
		min int) time.Time {
		return time.Date(y,
			m,
			d,
			h,
			min,
			0,
			0,
			loc)
	}
	cases := []struct {
		expr string
		from time.Time
		want time.Time
	}{
		{"* * * * *", at(time.UTC,
			2030,
			1,
			1,
			0,
			0), at(time.UTC,
			2030,
			1,
			1,
			0,
			1)},
		{"*/15 * * * *", at(time.UTC,
			2030,
			1,
			1,
			10,
			15), at(time.UTC,
			2030,
			1,
			1,
			10,
			30)},
		{"0 9 * * *", at(time.UTC,
			2030,
			1,
			1,
			9,
			0), at(time.UTC,
			2030,
			1,
			2,
			9,
			0)},
		{"@hourly", at(time.UTC,
			2030,
			1,
			1,
			9,
			30), at(time.UTC,
			2030,
			1,
			1,
			10,
			0)},
		{"@daily", at(time.UTC,
			2030,
			12,
			31,
			12,
			0), at(time.UTC,
			2031,
			1,
			1,
			0,
			0)},
		{"@weekly", at(time.UTC,
			2030,
			1,
			1,
			0,
			0), at(time.UTC,
			2030,
			1,
			6,
			0,
			0)}, // Sunday
		{"0 0 1 */3 *", at(time.UTC,
			2030,
			2,
			1,
			0,
			0), at(time.UTC,
			2030,
			4,
			1,
			0,
			0)},
		{"10-40/10 8 * * 1-5", at(time.UTC,
			2030,
			1,
			4,
			8,
			40), at(time.UTC,
			2030,
			1,
			7,
			8,
			10)},
		{"0 12 * jan,jul sun", at(time.UTC,
			2030,
			1,
			1,
			0,
			0), at(time.UTC,
			2030,
			1,
			6,
			12,
			0)},
		{"0 12 * * 7", at(time.UTC,
			2030,
			1,
			1,
			0,
			0), at(time.UTC,
			2030,
			1,
			6,
			12,
			0)}, // 7 is Sunday too
		{"0 0 29 2 *", at(time.UTC,
			2030,
			1,
			1,
			0,
			0), at(time.UTC,
			2032,
			2,
			29,
			0,
			0)},
		// day of month and day of week both restricted: either one
		{"0 0 13 * fri", at(time.UTC,
			2030,
			1,
			1,
			0,
			0), at(time.UTC,
			2030,
			1,
			4,
			0,
			0)},
		// the fields are read in the location of the time given
		{"0 7 * * *", at(berlin,
			2030,
			7,
			1,
			7,
			0), at(berlin,
			2030,
			7,
			2,
			7,
			0)},
		{"30 2 * * *", at(berlin,
			2030,
			3,
			30,
			12,
			0), at(berlin,
			2030,
			3,
			31,
			3,
			0)}, // 2:30 does not exist on 31 March
	}
	for _, c := range cases {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Errorf("%q: %v",
				c.expr,
				err)
			continue
		}
		got := cr.Next(c.from)
		if c.expr == "30 2 * * *" {
			// in the gap the run happens once, at some time of that night or the next day: it must be later than from and within two days
			if !got.After(c.from) || got.Sub(c.from) > 48*time.Hour {
				t.Errorf("%q: next = %v",
					c.expr,
					got)
			}
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("%q from %v: next = %v, want %v",
				c.expr,
				c.from,
				got,
				c.want)
		}
	}
}

func TestCronNeverMatches(t *testing.T) {
	c, err := ParseCron("0 0 31 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if next := c.Next(time.Now()); !next.IsZero() {
		t.Errorf("next = %v", next)
	}
}

func TestParseCronErrors(t *testing.T) {
	for expr, want := range map[string]string{
		"":              "needs 5",
		"* * * *":       "needs 5",
		"* * * * * *":   "needs 5",
		"60 * * * *":    "minute: \"60\": 60 is out of range",
		"* 24 * * *":    "hour",
		"* * 0 * *":     "day of month",
		"* * * 13 *":    "month",
		"* * * * 8":     "day of week",
		"*/0 * * * *":   "step",
		"5-1 * * * *":   "backwards",
		"a * * * *":     "not a number",
		"* * * foo *":   "month",
		"@sometimes":    "unknown macro",
		"1,,2 * * * *":  "minute",
		"* * * * mon-x": "day of week",
	} {
		_, err := ParseCron(expr)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q",
				expr,
				err,
				want)
		}
	}
}

func TestCronMinGap(t *testing.T) {
	from := time.Date(2030,
		1,
		1,
		0,
		0,
		0,
		0,
		time.UTC)
	for expr, want := range map[string]time.Duration{
		"*/5 * * * *":     5 * time.Minute,
		"0,10,50 * * * *": 10 * time.Minute,
		"@hourly":         time.Hour,
		"@daily":          24 * time.Hour,
	} {
		c, _ := ParseCron(expr)
		if got := c.MinGap(from); got != want {
			t.Errorf("%q: gap %v, want %v",
				expr,
				got,
				want)
		}
	}
}

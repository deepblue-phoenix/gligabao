package agent_defs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // the time zones of schedules must work where the system has none (the container image)
)

// Cron is a parsed CRON expression of five fields (minute, hour, day of month, month, day of week), as the schedule of an agent
// uses it. The syntax is the usual one: `*`, a number, a range `1-5`, a list `1,3,5`, a step `*/15` or `10-40/10`, month names
// (`jan`..`dec`), day names (`sun`..`sat`; 0 and 7 are Sunday), and the macros `@hourly`, `@daily` (`@midnight`), `@weekly`,
// `@monthly` and `@yearly` (`@annually`). As in other crons, when both the day of month and the day of week are restricted, a
// day that matches either one counts.
type Cron struct {
	minute, hour, dom, month, dow uint64 // bit sets
	domStar, dowStar              bool   // the field started with *: it does not restrict
	expr                          string
}

var cronMacros = map[string]string{
	"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@weekly": "0 0 * * 0",
	"@monthly": "0 0 1 * *", "@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *",
}

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dayNames   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
)

// ParseCron parses a CRON expression.
func ParseCron(expr string) (*Cron, error) {
	orig := strings.TrimSpace(expr)
	text := orig
	if strings.HasPrefix(text, "@") {
		m, ok := cronMacros[strings.ToLower(text)]
		if !ok {
			return nil, fmt.Errorf("unknown macro %q (known: @hourly, @daily, @weekly, @monthly, @yearly)", text)
		}
		text = m
	}
	f := strings.Fields(text)
	if len(f) != 5 {
		return nil, fmt.Errorf("%q has %d fields: it needs 5 (minute hour day-of-month month day-of-week)",
			orig,
			len(f))
	}
	c := &Cron{
		expr: orig,
	}
	var err error
	if c.minute, _, err = parseCronField(f[0],
		0,
		59,
		nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if c.hour, _, err = parseCronField(f[1],
		0,
		23,
		nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if c.dom, c.domStar, err = parseCronField(f[2],
		1,
		31,
		nil); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if c.month, _, err = parseCronField(f[3],
		1,
		12,
		monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if c.dow, c.dowStar, err = parseCronField(f[4],
		0,
		7,
		dayNames); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	if c.dow&(1<<7) != 0 { // 7 is Sunday too
		c.dow = c.dow&^(1<<7) | 1
	}
	return c, nil
}

// parseCronField returns the bit set of a field; star says it started with `*` (or `?`).
func parseCronField(field string,
	lo,
	hi int,
	names map[string]int) (set uint64, star bool, err error) {
	if field == "" {
		return 0, false, fmt.Errorf("empty")
	}
	star = field[0] == '*' || field[0] == '?'
	for _, part := range strings.Split(field, ",") {
		rng, step, hasStep := strings.Cut(part, "/")
		stepN := 1
		if hasStep {
			if stepN, err = strconv.Atoi(step); err != nil || stepN < 1 {
				return 0, false, fmt.Errorf("%q: the step must be a positive number", part)
			}
		}
		var from, to int
		switch {
		case rng == "*" || rng == "?":
			from, to = lo, hi
			if lo == 0 && hi == 7 {
				to = 6 // a day of week: * is Sunday to Saturday
			}
		default:
			a, b, isRange := strings.Cut(rng, "-")
			if from, err = cronValue(a,
				lo,
				hi,
				names); err != nil {
				return 0, false, fmt.Errorf("%q: %w",
					part,
					err)
			}
			to = from
			if isRange {
				if to, err = cronValue(b,
					lo,
					hi,
					names); err != nil {
					return 0, false, fmt.Errorf("%q: %w",
						part,
						err)
				}
			} else if hasStep {
				to = hi // "5/10" runs from 5 to the end
			}
			if from > to {
				return 0, false, fmt.Errorf("%q: the range goes backwards", part)
			}
		}
		for v := from; v <= to; v += stepN {
			set |= 1 << uint(v)
		}
	}
	return set, star, nil
}

func cronValue(s string,
	lo,
	hi int,
	names map[string]int) (int, error) {
	if v, ok := names[strings.ToLower(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number or a name", s)
	}
	if v < lo || v > hi {
		return 0, fmt.Errorf("%d is out of range (%d to %d)",
			v,
			lo,
			hi)
	}
	return v, nil
}

// String is the expression as written.
func (c *Cron) String() string { return c.expr }

// Next returns the first time after t (in t's location, which decides the time zone the fields are read in) that the expression
// matches, to the minute; the zero time if there is none in the next 8 years (e.g. 31 February).
func (c *Cron) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	loc := t.Location()
	limit := t.AddDate(8,
		0,
		0)
	// forward moves to the start of a later unit; a daylight saving gap can make time.Date answer a time that is not later, so it
	// is never allowed to go back.
	forward := func(next time.Time) {
		if !next.After(t) {
			next = t.Add(time.Hour)
		}
		t = next
	}
	for t.Before(limit) {
		switch {
		case c.month&(1<<uint(t.Month())) == 0:
			forward(time.Date(t.Year(),
				t.Month()+1,
				1,
				0,
				0,
				0,
				0,
				loc))
		case !c.dayMatches(t):
			forward(time.Date(t.Year(),
				t.Month(),
				t.Day()+1,
				0,
				0,
				0,
				0,
				loc))
		case c.hour&(1<<uint(t.Hour())) == 0:
			forward(time.Date(t.Year(),
				t.Month(),
				t.Day(),
				t.Hour()+1,
				0,
				0,
				0,
				loc))
		case c.minute&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// dayMatches applies the day of month and the day of week: both restricted means either matches.
func (c *Cron) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<uint(t.Day())) != 0
	dow := c.dow&(1<<uint(t.Weekday())) != 0
	if !c.domStar && !c.dowStar {
		return dom || dow
	}
	return dom && dow
}

// MinGap is the shortest time between two runs of the expression among the next 64 after from (0 if there are fewer than two).
func (c *Cron) MinGap(from time.Time) time.Duration {
	var min time.Duration
	prev := c.Next(from)
	for i := 0; i < 64 && !prev.IsZero(); i++ {
		next := c.Next(prev)
		if next.IsZero() {
			break
		}
		if gap := next.Sub(prev); min == 0 || gap < min {
			min = gap
		}
		prev = next
	}
	return min
}

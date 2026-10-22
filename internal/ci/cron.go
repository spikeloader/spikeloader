package ci

import (
	"strconv"
	"strings"
	"time"
)

// cronField matches one field: "*", "5", "1-5", "*/15", "10-50/10" and comma lists.
func cronField(expr string, v, min, max int) bool {
	for _, part := range strings.Split(expr, ",") {
		rng, stepRaw, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepRaw)
			if err != nil || n <= 0 {
				continue
			}
			step = n
		}
		lo, hi := min, max
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			x, err := strconv.Atoi(a)
			if err != nil {
				continue
			}
			lo = x
			switch {
			case isRange:
				y, err := strconv.Atoi(b)
				if err != nil {
					continue
				}
				hi = y
			case hasStep:
				hi = max
			default:
				hi = x
			}
		}
		if v >= lo && v <= hi && (v-lo)%step == 0 {
			return true
		}
	}
	return false
}

// CronMatches reports whether a 5-field cron expression (minute hour day-of-month month
// day-of-week, local time) fires at t. Sunday is 0 or 7.
func CronMatches(expr string, t time.Time) bool {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return false
	}
	wd := int(t.Weekday())
	return cronField(f[0], t.Minute(), 0, 59) &&
		cronField(f[1], t.Hour(), 0, 23) &&
		cronField(f[2], t.Day(), 1, 31) &&
		cronField(f[3], int(t.Month()), 1, 12) &&
		(cronField(f[4], wd, 0, 7) || (wd == 0 && cronField(f[4], 7, 0, 7)))
}

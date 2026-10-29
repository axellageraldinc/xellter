// Package spend finds a client whose spending has jumped (ADR 0041). It is pure: the caller reads the request log.
//
// It alerts and never blocks: a budget that refuses requests is a routing knob, and the gateway has none (ADR 0017).
// The point is to notice a runaway loop within the hour instead of at the end of the month.
package spend

import "sort"

// Spike is a client that spent far more in the last hour than it usually does.
type Spike struct {
	Client   string
	HourIDR  float64 // spent in the last hour
	UsualIDR float64 // the usual spend per hour over the rest of the week
}

// Window is how many hours the baseline covers.
const Window = 7 * 24

// Find returns the clients whose last hour cost at least factor times their usual hour and at least floor rupiah.
// hour is the cost of the last hour per client, week the cost of the last seven days (so it includes the hour).
// The floor keeps a client that normally spends nothing from alerting on a few rupiah; the factor keeps a client that
// spends a lot from alerting on an ordinary busy hour. The worst spike comes first.
func Find(hour, week map[string]float64, factor, floor float64) []Spike {
	if factor <= 0 {
		return nil
	}
	var out []Spike
	for client, h := range hour {
		if h < floor {
			continue
		}
		usual := (week[client] - h) / float64(Window-1)
		if usual < 0 {
			usual = 0
		}
		if h >= factor*usual {
			out = append(out, Spike{Client: client, HourIDR: h, UsualIDR: usual})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HourIDR != out[j].HourIDR {
			return out[i].HourIDR > out[j].HourIDR
		}
		return out[i].Client < out[j].Client
	})
	return out
}

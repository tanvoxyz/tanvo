package schedule

import (
	"fmt"
	"testing"
)

var stressCadences = []Interval{{1, Day}, {2, Day}, {1, Week}, {2, Week}, {1, Month}, {3, Day}}

func stressShape(t *testing.T, tz string, timing Timing, anchor Date) shape {
	var rates []rateArg
	for i := range 200 {
		c := stressCadences[i%len(stressCadences)]
		rates = append(rates, rateArg{utc(2026, 1, 1).AddDate(0, 0, i*4), c.Count, c.Unit})
	}
	var phases []phaseArg
	for i := range 60 {
		p := phaseArg{cycles: intp(2 + i%3), code: fmt.Sprintf("p%d", i)}
		if i%2 == 1 {
			c := stressCadences[(i+2)%len(stressCadences)]
			p.count, p.unit = c.Count, c.Unit
		}
		phases = append(phases, p)
	}
	phases = append(phases, phaseArg{})
	return shape{
		name: fmt.Sprintf("%s %s %s", anchor, tz, timing), anchor: anchor, tz: tz, timing: timing,
		startsAt: anchor.Start(zone(t, tz)), rates: rates, phases: phases,
	}
}

// source: spec/services/billing/rate_cards/schedule_resume_stress_spec.rb
func TestScheduleResumeStress(t *testing.T) {
	asked := utc(2027, 7, 1)
	shapes := []shape{
		stressShape(t, "America/New_York", Arrears, date(2026, 1, 31)),
		stressShape(t, "America/New_York", Advance, date(2026, 1, 31)),
		stressShape(t, "Asia/Kolkata", Arrears, date(2024, 2, 29)),
		stressShape(t, "Europe/Paris", Advance, date(2026, 3, 15)),
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			cycles := walkShape(t, s, asked, nil)
			// source: spec/services/billing/rate_cards/schedule_resume_stress_spec.rb:98
			anchors, intervals, modes, inside := map[Date]bool{}, map[Interval]bool{}, map[bool]bool{}, 0
			for n, c := range cycles {
				anchors[c.Calendar.AnchorDate()] = true
				intervals[c.Calendar.Interval()] = true
				modes[c.Phase.Override == nil] = true
				if c.Phase.Override != nil && n > 0 && cycles[n-1].Phase.Code == c.Phase.Code {
					inside++
				}
			}
			if len(cycles) <= 50 || len(anchors) <= 30 || len(intervals) != len(stressCadences) || len(modes) != 2 || inside <= 15 {
				t.Fatalf("fixture: got = %d cycles, %d anchors, %d intervals, %d modes, %d inside", len(cycles), len(anchors), len(intervals), len(modes), inside)
			}
			// source: spec/services/billing/rate_cards/schedule_resume_stress_spec.rb:117
			w := s.walker(t)
			for _, c := range cycles {
				at := c.StartedAt
				if got, want := stateOf(w.Resume(&at)), stateOf(c); got != want {
					t.Errorf("at %s: got = %s, want = %s", c.StartedAt, got, want)
				}
			}
			full := s.schedule(t, nil).SegmentsDueBy(asked)
			for n, c := range cycles {
				at := c.StartedAt
				// source: spec/services/billing/rate_cards/schedule_resume_stress_spec.rb:131
				if fingerprint(walkShape(t, s, asked, &at)) != fingerprint(cycles[n:]) {
					t.Errorf("cycles resumed at %d differ", n)
				}
				// source: spec/services/billing/rate_cards/schedule_resume_stress_spec.rb:145
				if segmentValues(s.schedule(t, &at).SegmentsDueBy(asked), 0) != segmentValues(full, c.Index) {
					t.Errorf("segments resumed at %d differ", c.Index)
				}
			}
		})
	}
}

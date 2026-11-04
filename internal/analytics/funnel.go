package analytics

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	minFunnelSteps     = 2
	maxFunnelSteps     = 10
	defaultWindowHours = 7 * 24
	maxWindowHours     = 90 * 24
	// maxFunnelRows bounds the matching events read into memory.
	maxFunnelRows = 2_000_000
)

// FunnelStep is one step: an event name, optionally narrowed by props.
type FunnelStep struct {
	Name  string         `json:"name" jsonschema:"event name, e.g. paywall.shown"`
	Props map[string]any `json:"props,omitempty" jsonschema:"optional equality filter on this step's props, e.g. {\"placement\": \"onboarding\"}"`
}

// FunnelInput is the input of funnel.
type FunnelInput struct {
	App         string       `json:"app" jsonschema:"app name or numeric id (see list_apps)"`
	From        string       `json:"from,omitempty" jsonschema:"start, inclusive: YYYY-MM-DD (UTC) or RFC 3339; default 30 days before 'to'. The first step must happen in [from, to)"`
	To          string       `json:"to,omitempty" jsonschema:"end: YYYY-MM-DD (that whole UTC day included) or RFC 3339 (exclusive); default now"`
	Steps       []FunnelStep `json:"steps" jsonschema:"2 to 10 ordered steps"`
	By          string       `json:"by,omitempty" jsonschema:"user (default) or session: who must complete the steps"`
	WindowHours int          `json:"window_hours,omitempty" jsonschema:"max hours from the first step to the last, default 168 (7 days), max 2160"`
}

// FunnelStepResult is the outcome of one step.
type FunnelStepResult struct {
	Step                   int            `json:"step"`
	Name                   string         `json:"name"`
	Props                  map[string]any `json:"props,omitempty"`
	Count                  int64          `json:"count"`
	ConversionFromPrevious *float64       `json:"conversion_from_previous"`
	ConversionFromStart    *float64       `json:"conversion_from_start"`
}

// FunnelResult is the output of funnel.
type FunnelResult struct {
	App         AppRef             `json:"app"`
	Period      Period             `json:"period"`
	By          string             `json:"by"`
	WindowHours int                `json:"window_hours"`
	Steps       []FunnelStepResult `json:"steps"`
}

type funnelEvent struct {
	ts   int64
	mask uint32
}

// Funnel counts how many users (or sessions) went through the steps in
// order. An entity enters with any occurrence of step 1 in [from, to); later
// steps must follow strictly in (ts, id) order, each on a distinct event, no
// later than window_hours after that step 1. Every step-1 occurrence is tried
// and the deepest progress counts, so a later attempt can convert. Later
// steps may fall after 'to' as long as they are inside the window.
func (s *Service) Funnel(ctx context.Context, in FunnelInput) (FunnelResult, error) {
	sc, err := s.resolveScope(ctx, in.App, in.From, in.To)
	if err != nil {
		return FunnelResult{}, err
	}
	n := len(in.Steps)
	if n < minFunnelSteps || n > maxFunnelSteps {
		return FunnelResult{}, fmt.Errorf("a funnel needs %d to %d steps, got %d", minFunnelSteps, maxFunnelSteps, n)
	}
	entity := "user_id"
	switch in.By {
	case "", "user":
		in.By = "user"
	case "session":
		entity = "session_id"
	default:
		return FunnelResult{}, fmt.Errorf("by must be user or session, got %q", in.By)
	}
	if in.WindowHours < 0 || in.WindowHours > maxWindowHours {
		return FunnelResult{}, fmt.Errorf("window_hours must be between 1 and %d", maxWindowHours)
	}
	if in.WindowHours == 0 {
		in.WindowHours = defaultWindowHours
	}
	window := int64(in.WindowHours) * 3600 * 1000

	// One CASE per step sets bit i when the event matches step i.
	var cases, names []string
	var caseArgs, nameArgs []any
	seenName := map[string]bool{}
	for i, st := range in.Steps {
		if st.Name == "" || len(st.Name) > maxNameLen {
			return FunnelResult{}, fmt.Errorf("step %d: invalid event name %q", i+1, st.Name)
		}
		cond := "name = ?"
		args := []any{st.Name}
		pc, pargs, err := propsCond(st.Props)
		if err != nil {
			return FunnelResult{}, fmt.Errorf("step %d: %w", i+1, err)
		}
		if pc != "" {
			cond += " AND " + pc
			args = append(args, pargs...)
		}
		cases = append(cases, fmt.Sprintf("(CASE WHEN %s THEN %d ELSE 0 END)", cond, 1<<i))
		caseArgs = append(caseArgs, args...)
		if !seenName[st.Name] {
			seenName[st.Name] = true
			names = append(names, "?")
			nameArgs = append(nameArgs, st.Name)
		}
	}
	q := fmt.Sprintf(`SELECT ent, ts, mask FROM (
		SELECT %s AS ent, ts, id, %s AS mask FROM events
		WHERE app_id = ? AND ts >= ? AND ts < ? AND name IN (%s)
	) WHERE mask <> 0 ORDER BY ent, ts, id LIMIT ?`,
		entity, strings.Join(cases, " | "), strings.Join(names, ", "))
	args := append([]any{}, caseArgs...)
	args = append(args, sc.app.ID, sc.from, sc.to+window)
	args = append(args, nameArgs...)
	args = append(args, maxFunnelRows+1)

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return FunnelResult{}, err
	}
	defer rows.Close()

	reached := make([]int64, n) // reached[k] = entities that completed step k+1
	var cur string
	var evs []funnelEvent
	total := 0
	flush := func() {
		if d := funnelDepth(evs, n, window, sc.to); d > 0 {
			for k := 0; k < d; k++ {
				reached[k]++
			}
		}
		evs = evs[:0]
	}
	for rows.Next() {
		var ent string
		var e funnelEvent
		if err := rows.Scan(&ent, &e.ts, &e.mask); err != nil {
			return FunnelResult{}, err
		}
		if total++; total > maxFunnelRows {
			return FunnelResult{}, errors.New("too many matching events; narrow the range or the steps")
		}
		if ent != cur && len(evs) > 0 {
			flush()
		}
		cur = ent
		evs = append(evs, e)
	}
	if err := rows.Err(); err != nil {
		return FunnelResult{}, err
	}
	flush()

	res := FunnelResult{App: sc.app, Period: sc.period(), By: in.By, WindowHours: in.WindowHours}
	for k, st := range in.Steps {
		r := FunnelStepResult{Step: k + 1, Name: st.Name, Props: st.Props, Count: reached[k],
			ConversionFromStart: ratio(reached[k], reached[0])}
		if k > 0 {
			r.ConversionFromPrevious = ratio(reached[k], reached[k-1])
		}
		res.Steps = append(res.Steps, r)
	}
	return res, nil
}

// funnelDepth returns how many steps (0..n) one entity completed, over its
// events sorted by (ts, id). Starts must be before end; for a fixed start,
// taking the earliest match of each next step is optimal.
func funnelDepth(evs []funnelEvent, n int, window, end int64) int {
	best := 0
	for i, start := range evs {
		if start.mask&1 == 0 || start.ts >= end {
			continue
		}
		deadline := start.ts + window
		depth := 1
		for j := i + 1; j < len(evs) && depth < n && evs[j].ts <= deadline; j++ {
			if evs[j].mask&(1<<depth) != 0 {
				depth++
			}
		}
		if depth > best {
			best = depth
			if best == n {
				break
			}
		}
	}
	return best
}

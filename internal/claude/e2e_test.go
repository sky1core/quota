package claude

import (
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/render"
)

// TestE2E_ScreenToRenderedText wires the REAL parser to the REAL renderer, so the
// producer/consumer contract cannot drift behind hand-written fixtures (a
// consumer test that builds its own map stays green even if this parser changes
// the windows shape). It also pins the point of the design: a period rename in
// Claude's own /usage text reaches the output verbatim — no code change here, and
// no invented vocabulary anywhere.
func TestE2E_ScreenToRenderedText(t *testing.T) {
	cases := []struct {
		name       string
		screen     string
		wantRows   []string
		absentRows []string
	}{
		{
			name: "today's real screen",
			screen: "Current session\n████   8% used\nResets 5:50pm (Asia/Seoul)\n" +
				"Current week (all models)\n█   2% used\nResets Jul 20 at 12pm (Asia/Seoul)\n" +
				"Current week (Fable)\n█▌   3% used\nResets Jul 20 at 12pm (Asia/Seoul)\n",
			wantRows: []string{"Session", "Week", "Fable"},
		},
		{
			name: "Claude renames the period -> output follows, no code change",
			screen: "Current session\n████   8% used\n" +
				"Current 5 days (all models)\n█   2% used\n",
			wantRows:   []string{"Session", "5 days"},
			absentRows: []string{"Week", "Weekly"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q, err := parseCaptured(c.screen)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := render.Text(map[string]any{"claude": q})
			for _, want := range c.wantRows {
				if !strings.Contains(got, want) {
					t.Errorf("output missing row %q:\n%s", want, got)
				}
			}
			for _, no := range c.absentRows {
				if strings.Contains(got, no) {
					t.Errorf("output must not invent %q:\n%s", no, got)
				}
			}
		})
	}
}

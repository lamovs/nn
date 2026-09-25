package shot

import (
	"io"
	"testing"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/config"
)

type modeAsker bool

func (a modeAsker) Interactive() bool        { return bool(a) }
func (modeAsker) Ask(string) (string, error) { return "once", nil }

func TestPrepareDeliveryModes(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		tty        bool
		want       string
	}{
		{"default interactive", "", true, "background"},
		{"default detached", "", false, "background"},
		{"explicit wait", "wait", false, "wait"},
		{"auto interactive", "auto", true, "wait"},
		{"auto detached", "auto", false, "background"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.AI.Consent["claude"] = ai.ConsentAlways
			plan, err := Prepare(&cfg, ai.Overrides{AI: true}, tc.mode, modeAsker(tc.tty), io.Discard)
			if err != nil || plan == nil {
				t.Fatalf("prepare: %v", err)
			}
			if plan.Mode != tc.want {
				t.Fatalf("mode=%q want %q", plan.Mode, tc.want)
			}
		})
	}
}

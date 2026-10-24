package dental

import (
	"fmt"

	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
)

// Tooth states stored in tooth_status.state.
const (
	Healthy   = "healthy"
	Filled    = "filled"
	RootCanal = "root_canal"
	Crowned   = "crowned"
	Extracted = "extracted"
	Implant   = "implant"
)

var ValidStates = []string{Healthy, Filled, RootCanal, Crowned, Extracted, Implant}

// transition is the rule for one treatment code: the states it may be applied to and
// the state the tooth ends up in. Empty result means the state does not change.
type transition struct {
	from          map[string]bool
	result        string
	permanentOnly bool
}

func set(states ...string) map[string]bool {
	m := make(map[string]bool, len(states))
	for _, s := range states {
		m[s] = true
	}
	return m
}

var present = set(Healthy, Filled, RootCanal, Crowned, Implant)

var transitions = map[string]transition{
	"EXAM":       {from: set(ValidStates...)},
	"CLEANING":   {from: present},
	"FILLING":    {from: set(Healthy, Filled, RootCanal), result: Filled},
	"ROOT_CANAL": {from: set(Healthy, Filled), result: RootCanal},
	"CROWN":      {from: set(Healthy, Filled, RootCanal, Implant), result: Crowned},
	"EXTRACTION": {from: set(Healthy, Filled, RootCanal, Crowned), result: Extracted},
	"IMPLANT":    {from: set(Extracted), result: Implant, permanentOnly: true},
}

var stateNames = map[string]string{
	Healthy: "sağlam", Filled: "dolgulu", RootCanal: "kanal tedavili",
	Crowned: "kuronlu", Extracted: "çekilmiş", Implant: "implantlı",
}

// Apply validates applying code to a tooth in state and returns the resulting state.
// Codes without a tooth rule (e.g. general procedures) are rejected when a tooth is given.
func Apply(code string, tooth int, state string) (string, error) {
	tr, ok := transitions[code]
	if !ok {
		return "", httpx.Unprocessable("INVALID_TOOTH_PROCEDURE", fmt.Sprintf("%s işlemi dişe uygulanamaz", code))
	}
	if tr.permanentOnly && IsPrimary(tooth) {
		return "", httpx.Unprocessable("INVALID_TOOTH_PROCEDURE",
			fmt.Sprintf("%s işlemi süt dişine (%d) uygulanamaz", code, tooth))
	}
	if !tr.from[state] {
		return "", httpx.Unprocessable("INVALID_TOOTH_PROCEDURE",
			fmt.Sprintf("%d numaralı diş %s; %s işlemi uygulanamaz", tooth, stateNames[state], code))
	}
	if tr.result == "" {
		return state, nil
	}
	return tr.result, nil
}

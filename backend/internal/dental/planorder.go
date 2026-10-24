package dental

import (
	"errors"
	"fmt"

	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
)

// ItemInput is one requested plan item. DependsOn is the 1-based index of another
// item in the same request.
type ItemInput struct {
	Tooth         *int   `json:"tooth"`
	TreatmentCode string `json:"treatment_code"`
	DependsOn     *int   `json:"depends_on"`
}

// implicitDeps: on the same tooth, one of the values must be done before the key.
var implicitDeps = map[string][]string{
	"CROWN":   {"ROOT_CANAL", "IMPLANT"},
	"IMPLANT": {"EXTRACTION"},
}

// ResolveDependencies returns, per item, the 0-based index it depends on (-1 for none).
// Explicit dependencies win; otherwise clinical rules add one (kanal → kuron).
func ResolveDependencies(items []ItemInput) ([]int, error) {
	deps := make([]int, len(items))
	for i, it := range items {
		deps[i] = -1
		if it.DependsOn != nil {
			d := *it.DependsOn - 1
			if d < 0 || d >= len(items) || d == i {
				return nil, httpx.Unprocessable("INVALID_DEPENDENCY",
					fmt.Sprintf("%d. kalemin bağımlılığı geçersiz", i+1))
			}
			deps[i] = d
			continue
		}
		if it.Tooth == nil {
			continue
		}
		for _, pre := range implicitDeps[it.TreatmentCode] {
			for j, other := range items {
				if j != i && other.TreatmentCode == pre && other.Tooth != nil && *other.Tooth == *it.Tooth {
					deps[i] = j
				}
			}
			if deps[i] >= 0 {
				break
			}
		}
	}
	return deps, nil
}

// TopoOrder orders items so that every item comes after its dependency, keeping the
// request order where possible. It fails on dependency cycles.
func TopoOrder(deps []int) ([]int, error) {
	n := len(deps)
	indeg := make([]int, n)
	children := make([][]int, n)
	for i, d := range deps {
		if d >= 0 {
			indeg[i]++
			children[d] = append(children[d], i)
		}
	}
	order := make([]int, 0, n)
	done := make([]bool, n)
	for len(order) < n {
		next := -1
		for i := range n {
			if !done[i] && indeg[i] == 0 {
				next = i
				break
			}
		}
		if next < 0 {
			return nil, httpx.Unprocessable("DEPENDENCY_CYCLE", "plan kalemleri arasında döngüsel bağımlılık var")
		}
		done[next] = true
		order = append(order, next)
		for _, c := range children[next] {
			indeg[c]--
		}
	}
	return order, nil
}

// SimulateTeeth applies items in order on top of the current chart and rejects plans that
// contain clinically impossible steps (e.g. dolgu on a tooth the plan already extracts).
func SimulateTeeth(items []ItemInput, order []int, chart map[int]string) error {
	state := make(map[int]string, len(chart))
	for k, v := range chart {
		state[k] = v
	}
	for _, i := range order {
		it := items[i]
		if it.Tooth == nil {
			continue
		}
		cur, ok := state[*it.Tooth]
		if !ok {
			cur = Healthy
		}
		next, err := Apply(it.TreatmentCode, *it.Tooth, cur)
		if err != nil {
			var he *httpx.Error
			if errors.As(err, &he) {
				return httpx.Unprocessable(he.Code, fmt.Sprintf("%d. kalem: %s", i+1, he.Message))
			}
			return err
		}
		state[*it.Tooth] = next
	}
	return nil
}

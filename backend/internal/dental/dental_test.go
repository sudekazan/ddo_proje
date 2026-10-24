package dental

import (
	"errors"
	"slices"
	"testing"

	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
)

func TestValidFDI(t *testing.T) {
	valid := []int{11, 18, 21, 28, 31, 38, 41, 48, 51, 55, 61, 65, 71, 75, 81, 85}
	invalid := []int{0, 9, 10, 19, 20, 49, 50, 56, 60, 66, 86, 90, 91, 100}
	for _, v := range valid {
		if !ValidFDI(v) {
			t.Errorf("ValidFDI(%d) = false, want true", v)
		}
	}
	for _, v := range invalid {
		if ValidFDI(v) {
			t.Errorf("ValidFDI(%d) = true, want false", v)
		}
	}
	if got := len(AllPermanent()) + len(AllPrimary()); got != 52 {
		t.Fatalf("chart has %d teeth, want 32+20", got)
	}
}

func code(err error) string {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

func TestApplyRejectsFillingOnExtractedTooth(t *testing.T) {
	if _, err := Apply("FILLING", 16, Extracted); code(err) != "INVALID_TOOTH_PROCEDURE" {
		t.Fatalf("expected rejection, got %v", err)
	}
	if s, err := Apply("FILLING", 16, Healthy); err != nil || s != Filled {
		t.Fatalf("filling healthy tooth: %v %v", s, err)
	}
}

func TestApplyImplantRules(t *testing.T) {
	if _, err := Apply("IMPLANT", 36, Healthy); err == nil {
		t.Fatal("implant on present tooth must fail")
	}
	if _, err := Apply("IMPLANT", 75, Extracted); err == nil {
		t.Fatal("implant on primary tooth must fail")
	}
	if s, err := Apply("IMPLANT", 36, Extracted); err != nil || s != Implant {
		t.Fatalf("implant: %v %v", s, err)
	}
}

func TestApplyExamKeepsState(t *testing.T) {
	for _, st := range ValidStates {
		if s, err := Apply("EXAM", 11, st); err != nil || s != st {
			t.Fatalf("exam on %s: %v %v", st, s, err)
		}
	}
}

func intp(v int) *int { return &v }

func TestCrownImplicitlyDependsOnRootCanal(t *testing.T) {
	items := []ItemInput{
		{Tooth: intp(46), TreatmentCode: "CROWN"},
		{Tooth: intp(46), TreatmentCode: "ROOT_CANAL"},
		{Tooth: intp(11), TreatmentCode: "FILLING"},
	}
	deps, err := ResolveDependencies(items)
	if err != nil {
		t.Fatal(err)
	}
	if deps[0] != 1 {
		t.Fatalf("crown should depend on root canal, deps=%v", deps)
	}
	order, err := TopoOrder(deps)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 0, 2}; !slices.Equal(order, want) {
		t.Fatalf("order=%v want %v", order, want)
	}
	if err := SimulateTeeth(items, order, nil); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
}

func TestTopoOrderDetectsCycle(t *testing.T) {
	if _, err := TopoOrder([]int{1, 2, 0}); code(err) != "DEPENDENCY_CYCLE" {
		t.Fatalf("expected cycle error, got %v", err)
	}
}

func TestSimulateRejectsFillingAfterPlannedExtraction(t *testing.T) {
	items := []ItemInput{
		{Tooth: intp(26), TreatmentCode: "EXTRACTION"},
		{Tooth: intp(26), TreatmentCode: "FILLING"},
	}
	deps, _ := ResolveDependencies(items)
	order, _ := TopoOrder(deps)
	if err := SimulateTeeth(items, order, nil); code(err) != "INVALID_TOOTH_PROCEDURE" {
		t.Fatalf("expected rejection, got %v", err)
	}
}

func TestSimulateUsesCurrentChart(t *testing.T) {
	items := []ItemInput{{Tooth: intp(14), TreatmentCode: "ROOT_CANAL"}}
	if err := SimulateTeeth(items, []int{0}, map[int]string{14: Extracted}); err == nil {
		t.Fatal("root canal on extracted tooth must be rejected")
	}
}

func TestInvalidExplicitDependency(t *testing.T) {
	items := []ItemInput{{TreatmentCode: "CLEANING", DependsOn: intp(1)}}
	if _, err := ResolveDependencies(items); code(err) != "INVALID_DEPENDENCY" {
		t.Fatalf("self dependency must fail, got %v", err)
	}
}

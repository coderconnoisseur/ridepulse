package store

import (
	"testing"

	"ridepulse/services/assignment-service/internal/domain"
)

func TestPutMatchedDedupes(t *testing.T) {
	m := NewMemory()
	first := domain.Assignment{RideID: "r1", DriverID: "d1", MatchedAT: 1, LatencyMs: 10}
	if !m.PutMatched(first) {
		t.Fatal("first PutMatched should be new")
	}
	if m.PutMatched(domain.Assignment{RideID: "r1", DriverID: "d2"}) {
		t.Fatal("duplicate PutMatched should not be new")
	}
	got, ok := m.Get("r1")
	if !ok || got != first {
		t.Fatalf("Get = %+v, %v; want original %+v", got, ok, first)
	}
	if m.Count() != 1 {
		t.Fatalf("Count = %d, want 1", m.Count())
	}
}

func TestMatchedOverridesUnmatched(t *testing.T) {
	m := NewMemory()
	if !m.PutUnmatched("r1", "no_drivers", 5) {
		t.Fatal("PutUnmatched should be new")
	}
	if !m.PutMatched(domain.Assignment{RideID: "r1", DriverID: "d1"}) {
		t.Fatal("PutMatched after unmatched should be new")
	}
	st, ok := m.Status("r1")
	if !ok || st.Status != domain.StatusMatched || st.Assignment.DriverID != "d1" {
		t.Fatalf("Status = %+v, %v; want matched by d1", st, ok)
	}
	if m.Count() != 1 {
		t.Fatalf("Count = %d, want 1", m.Count())
	}
}

func TestUnmatchedIgnoredAfterMatchedOrUnmatched(t *testing.T) {
	m := NewMemory()
	m.PutMatched(domain.Assignment{RideID: "r1", DriverID: "d1"})
	if m.PutUnmatched("r1", "no_drivers", 5) {
		t.Fatal("PutUnmatched after matched should be ignored")
	}
	if st, _ := m.Status("r1"); st.Status != domain.StatusMatched {
		t.Fatalf("status = %q, want matched", st.Status)
	}

	if !m.PutUnmatched("r2", "no_drivers", 7) {
		t.Fatal("first PutUnmatched should be new")
	}
	if m.PutUnmatched("r2", "match_deadline", 9) {
		t.Fatal("duplicate PutUnmatched should be ignored")
	}
	st, _ := m.Status("r2")
	if st.Reason != "no_drivers" || st.FailedAt != 7 {
		t.Fatalf("Status = %+v, want original reason and failedAt", st)
	}
}

func TestCountAndGetMatchedOnly(t *testing.T) {
	m := NewMemory()
	m.PutUnmatched("r1", "no_drivers", 5)
	if m.Count() != 0 {
		t.Fatalf("Count = %d, want 0 (unmatched not counted)", m.Count())
	}
	if _, ok := m.Get("r1"); ok {
		t.Fatal("Get should not return an unmatched ride")
	}
	if _, ok := m.Status("missing"); ok {
		t.Fatal("Status should report unknown ride as missing")
	}
}

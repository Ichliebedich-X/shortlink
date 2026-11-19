package bloom

import "testing"

func TestPositionsInRangeAndStable(t *testing.T) {
	f := &Filter{bits: 1 << 10, k: 7}

	p1 := f.positions("abc123")
	p2 := f.positions("abc123")

	if len(p1) != 7 {
		t.Fatalf("positions length = %d, want 7", len(p1))
	}

	for i := range p1 {
		if p1[i] != p2[i] {
			t.Fatalf("positions not stable at %d: %d != %d", i, p1[i], p2[i])
		}
		if p1[i] >= f.bits {
			t.Fatalf("position %d out of range: %d >= %d", i, p1[i], f.bits)
		}
	}
}

func TestDifferentCodesDiffer(t *testing.T) {
	f := &Filter{bits: 1 << 10, k: 7}

	a := f.positions("abc")
	b := f.positions("abd")

	same := 0
	for i := range a {
		if a[i] == b[i] {
			same++
		}
	}
	if same == len(a) {
		t.Fatal("different codes produced identical positions")
	}
}

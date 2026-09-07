package strength

import "testing"

// Label bands are the shared contract between the chat client and
// v2vctl: both assess through Assess, so bands cannot drift per binary.
func TestLabelBands(t *testing.T) {
	cases := map[int]string{0: "yếu", 1: "yếu", 2: "trung bình", 3: "mạnh", 4: "rất mạnh", 9: "rất mạnh"}
	for score, want := range cases {
		if got := label(score); got != want {
			t.Errorf("label(%d) = %q, want %q", score, got, want)
		}
	}
}

func TestAssessWeakAndCapped(t *testing.T) {
	weak := Assess("password123", nil)
	if !weak.Weak || weak.Score > 1 || weak.Label != "yếu" {
		t.Fatalf("expected weak/yếu, got %+v", weak)
	}
	strong := Assess("correct horse battery staple 9!", nil)
	if strong.Weak {
		t.Fatalf("expected strong, got %+v", strong)
	}
	huge := Assess("correct horse battery staple 9! extra entropy padding here 123456", nil)
	if !huge.Capped || huge.Bits != 128 {
		t.Fatalf("expected 128-bit clamp, got %+v", huge)
	}
}

package passprompt

import (
	"errors"
	"strings"
	"testing"
)

func script(lines ...string) func() (string, error) {
	i := 0
	return func() (string, error) {
		if i >= len(lines) {
			return "", errors.New("hết script")
		}
		l := lines[i]
		i++
		if strings.HasPrefix(l, "\x00err") {
			return "", errors.New("lỗi đọc")
		}
		return l, nil
	}
}

func TestFormatBits(t *testing.T) {
	if got := FormatBits(42); got != "42" {
		t.Errorf("FormatBits(42) = %q", got)
	}
	if got := FormatBits(12.34); got != "12.3" {
		t.Errorf("FormatBits(12.34) = %q", got)
	}
	if got := FormatBits(0); got != "0" {
		t.Errorf("FormatBits(0) = %q", got)
	}
}

func TestMeterBar(t *testing.T) {
	full := MeterBar(1)
	if got := len([]rune(full)); got != MeterWidth {
		t.Errorf("bar width = %d, want %d", got, MeterWidth)
	}
	if strings.Count(full, "█") != MeterWidth {
		t.Errorf("MeterBar(1) = %q, want full", full)
	}
	empty := MeterBar(0)
	if strings.Count(empty, "░") != MeterWidth {
		t.Errorf("MeterBar(0) = %q, want empty", empty)
	}
	half := MeterBar(0.5)
	if strings.Count(half, "█") != MeterWidth/2 {
		t.Errorf("MeterBar(0.5) = %q, want half", half)
	}
	if MeterBar(-1) != empty || MeterBar(2) != full {
		t.Error("MeterBar must clamp outside [0,1]")
	}
}

func TestDoubleEntryPiped(t *testing.T) {
	got, err := DoubleEntryPiped(script("same phrase here", "same phrase here"), 0, nil)
	if err != nil || got != "same phrase here" {
		t.Errorf("match = %q, %v", got, err)
	}
	got, err = DoubleEntryPiped(script("one", "two", "same phrase here", "same phrase here"), 3, nil)
	if err != nil || got != "same phrase here" {
		t.Errorf("retry match = %q, %v", got, err)
	}
	if _, err := DoubleEntryPiped(script("a", "b", "c", "d", "e", "f", "g", "h"), 3, nil); err != ErrMismatch {
		t.Errorf("exhaustion = %v, want ErrMismatch", err)
	}
	if _, err := DoubleEntryPiped(script("", "", "", "", "", ""), 3, nil); err != ErrEmpty {
		t.Errorf("blank = %v, want ErrEmpty", err)
	}
	if _, err := DoubleEntryPiped(script("\x00err"), 3, nil); err == nil {
		t.Error("read error must abort")
	}
	var prompts []string
	p := func(first bool, round, max int) {
		if first {
			prompts = append(prompts, "first")
		} else {
			prompts = append(prompts, "again")
		}
	}
	if _, err := DoubleEntryPiped(script("x", "x"), 3, p); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 || prompts[0] != "first" || prompts[1] != "again" {
		t.Errorf("prompt order = %v", prompts)
	}
}

func TestSinglePiped(t *testing.T) {
	if _, err := SinglePiped(script(""), false); err != ErrEmpty {
		t.Errorf("blank = %v, want ErrEmpty", err)
	}
	got, err := SinglePiped(script(""), true)
	if err != nil || got != "" {
		t.Errorf("allowEmpty = %q, %v", got, err)
	}
}

func TestExpectPiped(t *testing.T) {
	var prompts int
	got, err := ExpectPiped(script("wrong", "right"), "right", 3, func(_, _ int) { prompts++ })
	if err != nil || got != "right" {
		t.Errorf("retry match = %q, %v", got, err)
	}
	if prompts != 2 {
		t.Errorf("onPrompt calls = %d, want 2", prompts)
	}
	if _, err := ExpectPiped(script("a", "b"), "z", 2, nil); err != ErrMismatch {
		t.Errorf("exhaustion = %v, want ErrMismatch", err)
	}
	if _, err := ExpectPiped(script("\x00err"), "z", 3, nil); err == nil {
		t.Error("read error must abort")
	}
	if _, err := ExpectPiped(script("z"), "z", 0, nil); err != nil {
		t.Errorf("non-positive rounds must default: %v", err)
	}
}

func TestReadLineKeepsRemainder(t *testing.T) {
	r := strings.NewReader("first\nsecond\n")
	first, err := ReadLine(r)
	if err != nil || first != "first" {
		t.Fatalf("first = %q, %v", first, err)
	}
	rest, err := ReadLine(r)
	if err != nil || rest != "second" {
		t.Errorf("no read-ahead: rest = %q, %v", rest, err)
	}
}

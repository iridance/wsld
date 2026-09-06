package wsl

import (
	"testing"
)

func TestParseMeminfo(t *testing.T) {
	in := `MemTotal:       8123456 kB
MemFree:         123456 kB
MemAvailable:   4096000 kB
Buffers:          50000 kB
`
	used, total, err := ParseMeminfo([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if total != 8123456/1024 {
		t.Fatalf("total = %d", total)
	}
	wantUsed := (8123456 - 4096000) / 1024
	if used != wantUsed {
		t.Fatalf("used = %d want %d", used, wantUsed)
	}
}

func TestParseMeminfoMissingFields(t *testing.T) {
	_, _, err := ParseMeminfo([]byte("MemTotal: 1024 kB\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseCPUStat(t *testing.T) {
	in := `cpu  100 0 50 850 0 0 0 0 0 0
cpu0 50 0 25 425 0 0 0 0 0 0
`
	idle, total, err := ParseCPUStat([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if idle != 850 || total != 1000 {
		t.Fatalf("idle=%d total=%d", idle, total)
	}
}

func TestCPUPercentFromDelta(t *testing.T) {
	pct := CPUPercentFromDelta(850, 1000, 870, 1100)
	if pct < 79.9 || pct > 80.1 {
		t.Fatalf("pct = %v", pct)
	}
	if CPUPercentFromDelta(100, 100, 100, 100) != 0 {
		t.Fatal("zero delta")
	}
}

func TestParseCPUStatBad(t *testing.T) {
	_, _, err := ParseCPUStat([]byte("not a stat\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}

package wsl

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// sampleSleep 兩次 /proc/stat 之間的間隔（測試可覆寫）。
var sampleSleep = func(d time.Duration) { time.Sleep(d) }

// BypassSampleSleepForTest 測試用：跳過 150ms 間隔，回傳 restore。
func BypassSampleSleepForTest() (restore func()) {
	prev := sampleSleep
	sampleSleep = func(time.Duration) {}
	return func() { sampleSleep = prev }
}

// SampleDistroUsage 在 distro 內讀兩次 /proc/stat（間隔約 150ms）與 /proc/meminfo。
// 任一步失敗回 0,0,0（呼叫端仍可回 200）。
func SampleDistroUsage(ctx context.Context, r Runner, distro string) (cpuPercent float64, usedMB, totalMB int) {
	out1, _, err := RunIn(ctx, r, distro, "root", nil, "cat", "/proc/stat")
	if err != nil {
		return 0, 0, 0
	}
	idle1, total1, err := ParseCPUStat(out1)
	if err != nil {
		return 0, 0, 0
	}
	sampleSleep(150 * time.Millisecond)
	out2, _, err := RunIn(ctx, r, distro, "root", nil, "cat", "/proc/stat")
	if err != nil {
		return 0, 0, 0
	}
	idle2, total2, err := ParseCPUStat(out2)
	if err != nil {
		return 0, 0, 0
	}
	cpuPercent = CPUPercentFromDelta(idle1, total1, idle2, total2)

	memOut, _, err := RunIn(ctx, r, distro, "root", nil, "cat", "/proc/meminfo")
	if err != nil {
		return cpuPercent, 0, 0
	}
	usedMB, totalMB, err = ParseMeminfo(memOut)
	if err != nil {
		return cpuPercent, 0, 0
	}
	return cpuPercent, usedMB, totalMB
}

// ParseMeminfo 從 /proc/meminfo 解析 used／total（MB）。
// used = MemTotal − MemAvailable（皆為 kB，再除以 1024）。
func ParseMeminfo(data []byte) (usedMB, totalMB int, err error) {
	var totalKB, availKB int64
	var haveTotal, haveAvail bool
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			totalKB, err = strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("MemTotal: %w", err)
			}
			haveTotal = true
		case "MemAvailable:":
			availKB, err = strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("MemAvailable: %w", err)
			}
			haveAvail = true
		}
	}
	if !haveTotal || !haveAvail {
		return 0, 0, fmt.Errorf("meminfo: missing MemTotal or MemAvailable")
	}
	totalMB = int(totalKB / 1024)
	usedMB = int((totalKB - availKB) / 1024)
	if usedMB < 0 {
		usedMB = 0
	}
	return usedMB, totalMB, nil
}

// ParseCPUStat 解析 /proc/stat 第一行 `cpu ` 的 idle 與 total jiffies。
// idle = idle + iowait（欄位 4、5，0-based 在 user 之後）；total = 所有欄位之和。
func ParseCPUStat(data []byte) (idle, total uint64, err error) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		// cpu user nice system idle iowait irq softirq steal guest guest_nice
		if len(fields) < 5 {
			return 0, 0, fmt.Errorf("cpu stat: too few fields")
		}
		var sum uint64
		for i := 1; i < len(fields); i++ {
			v, e := strconv.ParseUint(fields[i], 10, 64)
			if e != nil {
				return 0, 0, fmt.Errorf("cpu stat field %d: %w", i, e)
			}
			sum += v
			if i == 4 || i == 5 { // idle, iowait
				idle += v
			}
		}
		return idle, sum, nil
	}
	return 0, 0, fmt.Errorf("cpu stat: no cpu line")
}

// CPUPercentFromDelta 以兩次 ParseCPUStat 結果計算使用率（0–100）。
func CPUPercentFromDelta(idle1, total1, idle2, total2 uint64) float64 {
	if total2 <= total1 {
		return 0
	}
	idleDelta := float64(idle2 - idle1)
	totalDelta := float64(total2 - total1)
	busy := (1 - idleDelta/totalDelta) * 100
	if busy < 0 {
		return 0
	}
	if busy > 100 {
		return 100
	}
	return busy
}

package wsl

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidVHDSizeGB(t *testing.T) {
	for _, n := range []int{MinVHDSizeGB, 60, 1024, MaxVHDSizeGB} {
		if !ValidVHDSizeGB(n) {
			t.Errorf("%d should be valid", n)
		}
	}
	for _, n := range []int{-1, 0, MinVHDSizeGB - 1, MaxVHDSizeGB + 1} {
		if ValidVHDSizeGB(n) {
			t.Errorf("%d should be invalid", n)
		}
	}
}

func TestInstallWithVHDSize(t *testing.T) {
	r := &fakeRunner{out: map[string]result{
		"--install -d Ubuntu --name dev --vhd-size 60GB --no-launch": {},
		"--install -d Ubuntu --vhd-size 120GB --no-launch":           {},
	}}
	if err := Install(context.Background(), r, "Ubuntu", "dev", 60); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), r, "Ubuntu", "", 120); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), r, "Ubuntu", "dev", 1); err == nil {
		t.Fatal("too-small size must be rejected before calling wsl.exe")
	}
	if len(r.calls) != 2 {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestResizeDisk(t *testing.T) {
	r := &fakeRunner{out: map[string]result{"--manage dev --resize 60GB": {}}}
	if err := ResizeDisk(context.Background(), r, "dev", 60); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 || strings.Join(r.calls[0], " ") != "--manage dev --resize 60GB" {
		t.Fatalf("calls = %v", r.calls)
	}
	if err := ResizeDisk(context.Background(), r, "bad name", 60); err == nil {
		t.Fatal("invalid distro must be rejected")
	}
	if err := ResizeDisk(context.Background(), r, "dev", MaxVHDSizeGB+1); err == nil {
		t.Fatal("invalid size must be rejected")
	}
	if len(r.calls) != 1 {
		t.Fatalf("invalid input must not call wsl.exe: %v", r.calls)
	}
}

func TestResizeDiskReportsStdoutAndDetectsVHDInUse(t *testing.T) {
	// wsl.exe 把錯誤寫在 stdout（UTF-16LE），stderr 為空
	r := &fakeRunner{out: map[string]result{"--manage dev --resize 60GB": {
		stdout: string(utf16le("無法完成作業，因為 VHD 目前正在使用中。若要強制 WSL 停止使用： wsl.exe --shutdown\r\n錯誤碼： Wsl/Service/WSL_E_DISTRO_NOT_STOPPED\r\n")),
		err:    &ExitError{Code: -1},
	}}}
	err := ResizeDisk(context.Background(), r, "dev", 60)
	if err == nil || !strings.Contains(err.Error(), "VHD 目前正在使用中") {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, ErrVHDInUse) {
		t.Fatalf("err should be ErrVHDInUse: %v", err)
	}
}

func TestResizeDiskReportsStderr(t *testing.T) {
	r := &fakeRunner{out: map[string]result{"--manage dev --resize 8GB": {
		stderr: string(utf16le("The disk is too small for the data.\r\n")),
		err:    &ExitError{Code: 1},
	}}}
	err := ResizeDisk(context.Background(), r, "dev", 8)
	if err == nil || !strings.Contains(err.Error(), "too small") {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrVHDInUse) {
		t.Fatal("other failures are not ErrVHDInUse")
	}
}

// writeTestVHDX 產生只有 VHDX 標頭、region table 與 metadata 的最小檔案（足以讀出 Virtual Disk Size）。
func writeTestVHDX(t *testing.T, size uint64) string {
	t.Helper()
	const metaOff = 1 << 20
	buf := make([]byte, 2*metaOff)
	copy(buf, "vhdxfile")
	rt := buf[192*1024:]
	copy(rt, "regi")
	binary.LittleEndian.PutUint32(rt[8:], 2)
	// entry 0：BAT（不相干，確認會略過）
	copy(rt[16:], guidLE("2DC27766-F623-4200-9D64-115E9BFD4A08"))
	binary.LittleEndian.PutUint64(rt[32:], 2<<20)
	binary.LittleEndian.PutUint32(rt[40:], 1<<20)
	// entry 1：metadata region
	copy(rt[48:], guidLE("8B7CA206-4790-4B9A-B8FE-575F050F886E"))
	binary.LittleEndian.PutUint64(rt[64:], metaOff)
	binary.LittleEndian.PutUint32(rt[72:], metaOff)
	m := buf[metaOff:]
	copy(m, "metadata")
	binary.LittleEndian.PutUint16(m[10:], 2)
	// item 0：block size（不相干）
	copy(m[32:], guidLE("CAA16737-FA36-4D43-B3B6-33F0AA44E76B"))
	binary.LittleEndian.PutUint32(m[48:], 0x10000)
	binary.LittleEndian.PutUint32(m[52:], 8)
	// item 1：virtual disk size
	copy(m[64:], guidLE("2FA54224-CD1B-4876-B211-5DBED83BF4B8"))
	binary.LittleEndian.PutUint32(m[80:], 0x10008)
	binary.LittleEndian.PutUint32(m[84:], 8)
	binary.LittleEndian.PutUint64(m[0x10008:], size)
	p := filepath.Join(t.TempDir(), "ext4.vhdx")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVHDXVirtualSize(t *testing.T) {
	p := writeTestVHDX(t, 60<<30)
	got, err := VHDXVirtualSize(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != 60<<30 {
		t.Fatalf("got %d", got)
	}
}

func TestVHDXVirtualSizeRejectsNonVHDX(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.vhdx")
	if err := os.WriteFile(p, make([]byte, 300*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VHDXVirtualSize(p); err == nil {
		t.Fatal("expected error for non-VHDX file")
	}
	if _, err := VHDXVirtualSize(filepath.Join(t.TempDir(), "missing.vhdx")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestRunningFromWSL(t *testing.T) {
	for p, want := range map[string]bool{
		`\\wsl.localhost\Ubuntu-26.04\home\me\wsld-gui.exe`: true,
		`\\WSL$\Ubuntu\tmp\wsld-gui.exe`:                    true,
		`C:\Tools\wsld\wsld-gui.exe`:                        false,
		`\\server\share\wsld-gui.exe`:                       false,
	} {
		if got := RunningFromWSL(p); got != want {
			t.Errorf("RunningFromWSL(%q) = %v", p, got)
		}
	}
}

// 實機（Ubuntu-26.04、1 TB ext4、約 30 GB 資料）的 wsl.exe 輸出節錄：stdout 與 stderr 各一份相同內容。
const realTooSmall = "e2fsck 1.47.0 (5-Feb-2023)\r\nPass 1: Checking inodes, blocks, and sizes\r\nPass 5: Checking group summary information\r\n/dev/sdd: ***** FILE SYSTEM WAS MODIFIED *****\r\nresize2fs 1.47.0 (5-Feb-2023)\r\n/usr/sbin/resize2fs: New size smaller than minimum (18390839)\r\n無法調整磁碟。\r\n錯誤碼： Wsl/Service/E_FAIL\r\n"

func TestResizeDiskTooSmall(t *testing.T) {
	r := &fakeRunner{out: map[string]result{"--manage dev --resize 60GB": {
		stdout: string(utf16le(realTooSmall)),
		stderr: realTooSmall,
		err:    &ExitError{Code: -1},
	}}}
	err := ResizeDisk(context.Background(), r, "dev", 60)
	var ts *TooSmallError
	if !errors.As(err, &ts) {
		t.Fatalf("err should be *TooSmallError: %v", err)
	}
	// 18390839 × 4 KiB ≈ 70.2 GiB → 進位再留 1 GB 餘裕
	if ts.MinGB != 72 {
		t.Fatalf("MinGB = %d", ts.MinGB)
	}
	if strings.Count(err.Error(), "resize2fs 1.47.0") > 1 {
		t.Fatalf("stdout/stderr duplicate must be collapsed: %q", err.Error())
	}
}

func TestMinSizeGBFromBlocks(t *testing.T) {
	for _, tc := range []struct {
		blocks, bs int64
		want       int
	}{
		{18390839, 4096, 72}, // 70.16 GiB
		{18390785, 4096, 72},
		{2621440, 4096, 11}, // 剛好 10 GiB
		{0, 4096, 0},
	} {
		if got := minSizeGB(tc.blocks, tc.bs); got != tc.want {
			t.Errorf("minSizeGB(%d,%d) = %d, want %d", tc.blocks, tc.bs, got, tc.want)
		}
	}
}

func TestMinDiskGB(t *testing.T) {
	r := &fakeRunner{out: map[string]result{
		"-d dev -u root -- findmnt -n -o SOURCE /":  {stdout: "/dev/sdd\n"},
		"-d dev -u root -- stat -f -c %S /":         {stdout: "4096\n"},
		"-d dev -u root -- resize2fs -P /dev/sdd":   {stdout: "resize2fs 1.47.2 (1-Jan-2025)\nEstimated minimum size of the filesystem: 18390785\n"},
		"-d odd -u root -- findmnt -n -o SOURCE /":  {stdout: "/dev/sdd; rm -rf /\n"},
		"-d none -u root -- findmnt -n -o SOURCE /": {stdout: "/dev/sde\n"},
		"-d none -u root -- stat -f -c %S /":        {stdout: "4096\n"},
		"-d none -u root -- resize2fs -P /dev/sde":  {stdout: "resize2fs: Device or resource busy\n"},
	}}
	got, err := MinDiskGB(context.Background(), r, "dev")
	if err != nil || got != 72 {
		t.Fatalf("got %d %v", got, err)
	}
	if _, err := MinDiskGB(context.Background(), r, "odd"); err == nil {
		t.Fatal("unexpected device text must be rejected")
	}
	if _, err := MinDiskGB(context.Background(), r, "none"); err == nil {
		t.Fatal("missing estimate must be an error")
	}
}

func TestCompactDisk(t *testing.T) {
	r := &fakeRunner{out: map[string]result{
		"--manage dev --compact": {},
		"--manage busy --compact": {
			stdout: string(utf16le("無法完成作業，因為 VHD 目前正在使用中。\r\n錯誤碼： Wsl/Service/WSL_E_DISTRO_NOT_STOPPED\r\n")),
			err:    &ExitError{Code: -1},
		},
	}}
	if err := CompactDisk(context.Background(), r, "dev"); err != nil {
		t.Fatal(err)
	}
	if err := CompactDisk(context.Background(), r, "busy"); !errors.Is(err, ErrVHDInUse) {
		t.Fatalf("err = %v", err)
	}
	if err := CompactDisk(context.Background(), r, "bad name"); err == nil {
		t.Fatal("invalid name must be rejected")
	}
}

const listTwoRunning = "  NAME STATE VERSION\r\n* main Running 2\r\n  other Running 2\r\n  dev Stopped 2\r\n"

func TestWithFreeVHDDirectWhenFree(t *testing.T) {
	r := &fakeRunner{out: map[string]result{"--list --verbose": {stdout: string(utf16le(listTwoRunning))}}}
	calls := 0
	res, err := WithFreeVHD(context.Background(), r, "dev", FreeVHDOptions{}, func(context.Context) error { calls++; return nil })
	if err != nil || calls != 1 || res.Shutdown || len(res.Restarted) != 0 {
		t.Fatalf("res=%+v err=%v calls=%d", res, err, calls)
	}
	for _, c := range r.calls {
		if strings.Join(c, " ") == "--shutdown" {
			t.Fatal("must not shut down when the VHD is free")
		}
	}
}

func TestWithFreeVHDNeedsConsent(t *testing.T) {
	r := &fakeRunner{out: map[string]result{"--list --verbose": {stdout: string(utf16le(listTwoRunning))}}}
	calls := 0
	_, err := WithFreeVHD(context.Background(), r, "main", FreeVHDOptions{Primary: "other"}, func(context.Context) error { calls++; return nil })
	var ns *NeedsShutdownError
	if !errors.As(err, &ns) || strings.Join(ns.Running, ",") != "other,main" || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if _, err := WithFreeVHD(context.Background(), r, "main", FreeVHDOptions{Consent: true, SelfInWSL: true}, func(context.Context) error { return nil }); !errors.Is(err, ErrSelfInWSL) {
		t.Fatalf("self in wsl err=%v", err)
	}
}

func TestWithFreeVHDShutdownRetryAndRestart(t *testing.T) {
	r := &fakeRunner{out: map[string]result{
		"--list --verbose": {stdout: string(utf16le(listTwoRunning))},
		"--shutdown":       {},
		"-d main -- true":  {},
		"-d other -- true": {},
	}}
	calls := 0
	var stopped, woke []string
	op := func(context.Context) error {
		calls++
		if calls <= 2 { // 直接試被占用；關機後又被輪詢叫醒、仍被占用
			return fmt.Errorf("%w: busy", ErrVHDInUse)
		}
		return nil
	}
	res, err := WithFreeVHD(context.Background(), r, "dev", FreeVHDOptions{
		Consent: true, Primary: "other",
		BeforeShutdown: func(running []string) { stopped = running },
		AfterWake:      func(name string) { woke = append(woke, name) },
	}, func(ctx context.Context) error {
		// 未執行的目標會先直接試一次（被占用）
		return op(ctx)
	})
	if err != nil || !res.Shutdown || strings.Join(res.Restarted, ",") != "other,main" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if strings.Join(stopped, ",") != "other,main" || strings.Join(woke, ",") != "other,main" {
		t.Fatalf("stopped=%v woke=%v", stopped, woke)
	}
	shutdowns := 0
	for _, c := range r.calls {
		if strings.Join(c, " ") == "--shutdown" {
			shutdowns++
		}
	}
	// 直接試（占用）→ shutdown + op（占用，模擬被叫醒）→ shutdown + op（成功）
	if calls != 3 || shutdowns != 2 {
		t.Fatalf("calls=%d shutdowns=%d", calls, shutdowns)
	}
}

package wsl

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 虛擬磁碟（ext4.vhdx）容量上限的允許範圍（GB）。WSL 未指定時預設 1 TB。
const (
	MinVHDSizeGB = 8
	MaxVHDSizeGB = 16384
	// DefaultVHDSizeGB 是 wsld 新增機器時預設的磁碟上限（使用者可改；0 = 交給 WSL 預設）。
	DefaultVHDSizeGB = 60
)

// ValidVHDSizeGB 檢查磁碟上限是否在 MinVHDSizeGB..MaxVHDSizeGB 之間。
func ValidVHDSizeGB(n int) bool { return n >= MinVHDSizeGB && n <= MaxVHDSizeGB }

func vhdSizeArg(gb int) string { return strconv.Itoa(gb) + "GB" }

// ErrVHDInUse：WSL 仍掛著該 distro 的 VHD（WSL_E_DISTRO_NOT_STOPPED）。
// 實測 `wsl --terminate` 之後 VHD 仍掛在 WSL VM 上，要 `wsl --shutdown` 才會卸下。
var ErrVHDInUse = errors.New("wsl: the distro's virtual disk is still in use (needs wsl --shutdown)")

// ResizeDisk 執行 `wsl --manage <distro> --resize <n>GB`，調整 distro 虛擬磁碟的容量上限。
// VHD 必須未被 WSL 使用（通常要 `wsl --shutdown`），否則回傳包著 ErrVHDInUse 的錯誤；
// 縮小時已用空間須放得下，否則 wsl.exe 會回錯。
func ResizeDisk(ctx context.Context, r Runner, distro string, sizeGB int) error {
	if !ValidDistroName(distro) {
		return fmt.Errorf("invalid distro name %q", distro)
	}
	if !ValidVHDSizeGB(sizeGB) {
		return fmt.Errorf("invalid disk size %d GB (allowed %d–%d)", sizeGB, MinVHDSizeGB, MaxVHDSizeGB)
	}
	stdout, stderr, err := r.Run(ctx, nil, "--manage", distro, "--resize", vhdSizeArg(sizeGB))
	if err != nil {
		msg := combineOutput(DecodeOutput(stdout), DecodeOutput(stderr))
		if strings.Contains(msg, "WSL_E_DISTRO_NOT_STOPPED") {
			return fmt.Errorf("%w: %s", ErrVHDInUse, msg)
		}
		if m := tooSmallRe.FindStringSubmatch(msg); m != nil {
			blocks, _ := strconv.ParseInt(m[1], 10, 64)
			// WSL 的 ext4 一律 4 KiB 區塊
			return &TooSmallError{RequestedGB: sizeGB, MinGB: minSizeGB(blocks, 4096), Detail: msg}
		}
		return fmt.Errorf("wsl --manage %s --resize: %w: %s", distro, err, msg)
	}
	return nil
}

// combineOutput 合併 wsl.exe 的 stdout 與 stderr（它自己的訊息在 stdout；resize 時兩邊常是同一份 e2fsck 輸出）。
func combineOutput(stdout, stderr string) string {
	out, errText := strings.TrimSpace(stdout), strings.TrimSpace(stderr)
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	switch {
	case out == "":
		return errText
	case errText == "" || strings.Contains(norm(out), norm(errText)):
		return out
	case strings.Contains(norm(errText), norm(out)):
		return errText
	}
	return out + "\n" + errText
}

var (
	tooSmallRe   = regexp.MustCompile(`New size smaller than minimum \((\d+)\)`)
	estimatedRe  = regexp.MustCompile(`Estimated minimum size of the filesystem: (\d+)`)
	blockDevRe   = regexp.MustCompile(`^/dev/[a-z0-9]+$`)
	errNoMinimum = errors.New("wsl: could not estimate the minimum filesystem size")
)

// TooSmallError：縮小後放不下檔案系統（resize2fs 回報的最小區塊數換算為 GB）。
type TooSmallError struct {
	RequestedGB int
	MinGB       int    // 建議的最小上限（已進位並留 1 GB 餘裕）
	Detail      string // wsl.exe 原始輸出
}

func (e *TooSmallError) Error() string {
	return fmt.Sprintf("disk limit %d GB is smaller than the filesystem minimum (about %d GB)", e.RequestedGB, e.MinGB)
}

// minSizeGB 把 ext4 區塊數換成建議的最小上限 GB：無條件進位後再加 1 GB 餘裕
// （resize2fs -P 只是估計值，WSL 實際算出的最小值會略大）。
func minSizeGB(blocks, blockSize int64) int {
	if blocks <= 0 || blockSize <= 0 {
		return 0
	}
	b := blocks * blockSize
	return int((b+(1<<30)-1)>>30) + 1
}

// MinDiskGB 在執行中的 distro 內以 `resize2fs -P` 估計根檔案系統能縮到的最小上限（GB）。
// 會在 distro 內執行命令（停止中的 distro 會被喚醒），呼叫端只應對執行中的 distro 使用。
func MinDiskGB(ctx context.Context, r Runner, distro string) (int, error) {
	out, _, err := RunIn(ctx, r, distro, "root", nil, "findmnt", "-n", "-o", "SOURCE", "/")
	if err != nil {
		return 0, err
	}
	dev := strings.TrimSpace(DecodeOutput(out))
	if !blockDevRe.MatchString(dev) {
		return 0, fmt.Errorf("wsl: unexpected root device %q", dev)
	}
	out, _, err = RunIn(ctx, r, distro, "root", nil, "stat", "-f", "-c", "%S", "/")
	if err != nil {
		return 0, err
	}
	bs, err := strconv.ParseInt(strings.TrimSpace(DecodeOutput(out)), 10, 64)
	if err != nil || bs <= 0 {
		return 0, fmt.Errorf("wsl: unexpected block size %q", strings.TrimSpace(DecodeOutput(out)))
	}
	out, stderr, _ := RunIn(ctx, r, distro, "root", nil, "resize2fs", "-P", dev)
	m := estimatedRe.FindStringSubmatch(DecodeOutput(out) + "\n" + DecodeOutput(stderr))
	if m == nil {
		return 0, errNoMinimum
	}
	blocks, _ := strconv.ParseInt(m[1], 10, 64)
	return minSizeGB(blocks, bs), nil
}

// RunningFromWSL 判斷執行檔路徑是否位於 WSL 檔案系統（\\wsl.localhost\… 或 \\wsl$\…）。
// 此時 `wsl --shutdown` 會讓程式本身失去執行檔來源。
func RunningFromWSL(exe string) bool {
	p := strings.ToLower(strings.ReplaceAll(exe, "/", `\`))
	return strings.HasPrefix(p, `\\wsl.localhost\`) || strings.HasPrefix(p, `\\wsl$\`)
}

// guidLE 把標準字串格式的 GUID 轉成磁碟上的 mixed-endian 16 bytes。
func guidLE(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		panic("bad guid " + s)
	}
	out := make([]byte, 16)
	out[0], out[1], out[2], out[3] = b[3], b[2], b[1], b[0]
	out[4], out[5] = b[5], b[4]
	out[6], out[7] = b[7], b[6]
	copy(out[8:], b[8:])
	return out
}

var (
	vhdxMetadataRegion = guidLE("8B7CA206-4790-4B9A-B8FE-575F050F886E")
	vhdxVirtualDisk    = guidLE("2FA54224-CD1B-4876-B211-5DBED83BF4B8")
)

const (
	vhdxRegionTableOff = 192 * 1024
	vhdxRegionTableLen = 64 * 1024
	vhdxMaxEntries     = 2047
)

// VHDXVirtualSize 從 VHDX 標頭讀出虛擬磁碟容量（即上限；動態磁碟的實際檔案大小較小）。
// 只讀 region table 與 metadata，distro 執行中也能讀。
func VHDXVirtualSize(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sig := make([]byte, 8)
	if _, err := io.ReadFull(f, sig); err != nil || string(sig) != "vhdxfile" {
		return 0, errors.New("wsl: not a VHDX file: " + path)
	}
	rt := make([]byte, vhdxRegionTableLen)
	if _, err := f.ReadAt(rt, vhdxRegionTableOff); err != nil {
		return 0, fmt.Errorf("wsl: read VHDX region table: %w", err)
	}
	if string(rt[:4]) != "regi" {
		return 0, errors.New("wsl: bad VHDX region table")
	}
	n := int(binary.LittleEndian.Uint32(rt[8:]))
	if n > vhdxMaxEntries {
		return 0, errors.New("wsl: bad VHDX region count")
	}
	for i := 0; i < n; i++ {
		e := rt[16+32*i : 48+32*i]
		if !bytes.Equal(e[:16], vhdxMetadataRegion) {
			continue
		}
		off := int64(binary.LittleEndian.Uint64(e[16:]))
		ln := binary.LittleEndian.Uint32(e[24:])
		if ln < 32 || ln > 1<<24 {
			return 0, errors.New("wsl: bad VHDX metadata region")
		}
		m := make([]byte, ln)
		if _, err := f.ReadAt(m, off); err != nil {
			return 0, fmt.Errorf("wsl: read VHDX metadata: %w", err)
		}
		return vhdxMetadataVirtualSize(m)
	}
	return 0, errors.New("wsl: VHDX metadata region not found")
}

func vhdxMetadataVirtualSize(m []byte) (int64, error) {
	if string(m[:8]) != "metadata" {
		return 0, errors.New("wsl: bad VHDX metadata table")
	}
	cnt := int(binary.LittleEndian.Uint16(m[10:]))
	for j := 0; j < cnt && 64+32*j <= len(m); j++ {
		e := m[32+32*j : 64+32*j]
		if !bytes.Equal(e[:16], vhdxVirtualDisk) {
			continue
		}
		o := int(binary.LittleEndian.Uint32(e[16:]))
		if o < 0 || o+8 > len(m) {
			return 0, errors.New("wsl: bad VHDX virtual disk size offset")
		}
		v := binary.LittleEndian.Uint64(m[o:])
		if v == 0 || v > 1<<50 {
			return 0, errors.New("wsl: bad VHDX virtual disk size")
		}
		return int64(v), nil
	}
	return 0, errors.New("wsl: VHDX virtual disk size not found")
}

// CompactDisk 執行 `wsl --manage <distro> --compact`，把 VHDX 中已不再使用的空間還給 Windows。
// 與 ResizeDisk 相同，VHD 被 WSL 占用時回傳包著 ErrVHDInUse 的錯誤。
func CompactDisk(ctx context.Context, r Runner, distro string) error {
	if !ValidDistroName(distro) {
		return fmt.Errorf("invalid distro name %q", distro)
	}
	stdout, stderr, err := r.Run(ctx, nil, "--manage", distro, "--compact")
	if err != nil {
		msg := combineOutput(DecodeOutput(stdout), DecodeOutput(stderr))
		if strings.Contains(msg, "WSL_E_DISTRO_NOT_STOPPED") {
			return fmt.Errorf("%w: %s", ErrVHDInUse, msg)
		}
		return fmt.Errorf("wsl --manage %s --compact: %w: %s", distro, err, msg)
	}
	return nil
}

// ErrSelfInWSL：呼叫端（wsld／wsld-gui）的執行檔位於 WSL 檔案系統，不能關閉 WSL。
var ErrSelfInWSL = errors.New("wsl: refusing to shut WSL down while running from inside WSL")

// NeedsShutdownError：VHD 被 WSL 占用，需要 wsl --shutdown；Running 為會被停止的機器（主要機器在前）。
type NeedsShutdownError struct{ Running []string }

func (e *NeedsShutdownError) Error() string {
	return "wsl: the virtual disk is in use; WSL must be shut down (running: " + strings.Join(e.Running, ", ") + ")"
}

// FreeVHDOptions 控制 WithFreeVHD 的關機流程。
type FreeVHDOptions struct {
	Consent        bool                   // 使用者已同意 wsl --shutdown
	SelfInWSL      bool                   // 呼叫端位於 WSL 檔案系統：拒絕關機
	Primary        string                 // 主要機器：排在第一個重新啟動（引擎最快回來）
	BeforeShutdown func(running []string) // 關機前（例如停各機 docker）
	AfterWake      func(name string)      // 每台重新啟動後
}

// FreeVHDResult 回報是否關過 WSL、重新啟動了哪些機器。
type FreeVHDResult struct {
	Shutdown  bool
	Restarted []string
}

// WithFreeVHD 在 distro 的 VHD 未被 WSL 占用時執行 op（--resize／--compact 都需要）。
// 實測 `wsl --terminate` 不會卸下 VHD，只有 `wsl --shutdown` 會，所以：
//   - distro 未執行時先直接試；成功或非占用錯誤就結束。
//   - 否則未同意 → *NeedsShutdownError；呼叫端位於 WSL 內 → ErrSelfInWSL。
//   - 同意後：BeforeShutdown → wsl --shutdown → op（輪詢把 WSL 叫醒而又被占用時重試，最多 3 次）
//     → 把原本執行中的機器開回來（不論 op 成敗）。
func WithFreeVHD(ctx context.Context, r Runner, distro string, opts FreeVHDOptions, op func(context.Context) error) (FreeVHDResult, error) {
	res := FreeVHDResult{Restarted: []string{}}
	list, err := ListDistros(ctx, r)
	if err != nil {
		return res, err
	}
	d, ok := FindDistro(list, distro)
	if !ok {
		return res, fmt.Errorf("wsl: no distro %q", distro)
	}
	if !strings.EqualFold(d.State, "Running") {
		err := op(ctx)
		if err == nil || !errors.Is(err, ErrVHDInUse) {
			return res, err
		}
	}
	var running []string
	for _, x := range list {
		if strings.EqualFold(x.State, "Running") {
			running = append(running, x.Name)
		}
	}
	sort.SliceStable(running, func(i, j int) bool {
		return strings.EqualFold(running[i], opts.Primary) && !strings.EqualFold(running[j], opts.Primary)
	})
	if running == nil {
		running = []string{}
	}
	if !opts.Consent {
		return res, &NeedsShutdownError{Running: running}
	}
	if opts.SelfInWSL {
		return res, ErrSelfInWSL
	}
	if opts.BeforeShutdown != nil {
		opts.BeforeShutdown(running)
	}
	res.Shutdown = true
	var opErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := Shutdown(ctx, r); err != nil {
			opErr = err
			break
		}
		opErr = op(ctx)
		if !errors.Is(opErr, ErrVHDInUse) {
			break
		}
	}
	for _, name := range running {
		if err := Wake(ctx, r, name); err != nil {
			if opErr == nil {
				opErr = &RestartError{Name: name, Err: err}
			}
			continue
		}
		res.Restarted = append(res.Restarted, name)
		if opts.AfterWake != nil {
			opts.AfterWake(name)
		}
	}
	return res, opErr
}

// RestartError：操作完成，但原本執行中的機器無法重新啟動。
type RestartError struct {
	Name string
	Err  error
}

func (e *RestartError) Error() string {
	return "wsl: could not start " + e.Name + " again: " + e.Err.Error()
}
func (e *RestartError) Unwrap() error { return e.Err }

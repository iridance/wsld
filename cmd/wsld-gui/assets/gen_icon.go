//go:build ignore

// 產生 wsld 圖示：藍色圓角方塊 + 白色容器堆疊。
//
//	go run ./cmd/wsld-gui/assets/gen_icon.go
//
// 輸出 assets/tray.ico（16/20/24/32/40/48/64/256；≤64 為 BMP、256 為 PNG）
// 與 assets/icon.png（256）。
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// Windows 10/11 常見 DPI（100–200%）用到的尺寸階梯。
var sizes = []int{16, 20, 24, 32, 40, 48, 64, 256}

func draw(n int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	blue := color.NRGBA{0x25, 0x63, 0xeb, 0xff}
	white := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	f := float64(n) / 32.0
	r := 4 * f // 圓角半徑
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			dx, dy := 0.0, 0.0
			if fx < r {
				dx = r - fx
			} else if fx > float64(n)-r {
				dx = fx - (float64(n) - r)
			}
			if fy < r {
				dy = r - fy
			} else if fy > float64(n)-r {
				dy = fy - (float64(n) - r)
			}
			if dx*dx+dy*dy > r*r {
				continue
			}
			img.SetNRGBA(x, y, blue)
		}
	}
	// 以 32px 座標定義的白色方塊（三個 + 兩個 + 底座），再依比例放大
	boxes := [][4]float64{
		{8, 9, 14, 14}, {15, 9, 21, 14}, {22, 9, 26, 14},
		{8, 15, 14, 20}, {15, 15, 21, 20},
		{6, 21, 26, 24},
	}
	for _, b := range boxes {
		x0, y0, x1, y1 := int(b[0]*f+0.5), int(b[1]*f+0.5), int(b[2]*f+0.5), int(b[3]*f+0.5)
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				img.SetNRGBA(x, y, white)
			}
		}
	}
	return img
}

// encodeBMPIcon 寫入 ICO 用的 32bpp DIB（XOR + 1bpp AND mask）。
// 小尺寸用 BMP 較穩；LoadImage／Tray 對 PNG-in-ICO 較挑。
func encodeBMPIcon(img *image.NRGBA) []byte {
	n := img.Bounds().Dx()
	xorStride := n * 4
	andStride := ((n + 31) / 32) * 4 // 每列對齊 32-bit
	xorSize := xorStride * n
	andSize := andStride * n

	var b bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&b, le, uint32(40))       // biSize
	_ = binary.Write(&b, le, int32(n))         // biWidth
	_ = binary.Write(&b, le, int32(n*2))       // biHeight：XOR + AND
	_ = binary.Write(&b, le, uint16(1))        // biPlanes
	_ = binary.Write(&b, le, uint16(32))       // biBitCount
	_ = binary.Write(&b, le, uint32(0))        // biCompression = BI_RGB
	_ = binary.Write(&b, le, uint32(xorSize+andSize))
	_ = binary.Write(&b, le, int32(0))
	_ = binary.Write(&b, le, int32(0))
	_ = binary.Write(&b, le, uint32(0))
	_ = binary.Write(&b, le, uint32(0))

	// XOR：bottom-up，BGRA
	row := make([]byte, xorStride)
	for y := n - 1; y >= 0; y-- {
		for x := 0; x < n; x++ {
			c := img.NRGBAAt(x, y)
			i := x * 4
			row[i], row[i+1], row[i+2], row[i+3] = c.B, c.G, c.R, c.A
		}
		b.Write(row)
	}

	// AND mask：透明處為 1（遮罩），不透明為 0；bottom-up
	andRow := make([]byte, andStride)
	for y := n - 1; y >= 0; y-- {
		clear(andRow)
		for x := 0; x < n; x++ {
			if img.NRGBAAt(x, y).A < 128 {
				andRow[x/8] |= 0x80 >> uint(x%8)
			}
		}
		b.Write(andRow)
	}
	return b.Bytes()
}

func encodeFrame(img *image.NRGBA) []byte {
	if img.Bounds().Dx() >= 256 {
		var b bytes.Buffer
		must(png.Encode(&b, img))
		return b.Bytes()
	}
	return encodeBMPIcon(img)
}

func main() {
	dir := "cmd/wsld-gui/assets"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	frames := make([][]byte, len(sizes))
	var png256 []byte
	for i, n := range sizes {
		img := draw(n)
		frames[i] = encodeFrame(img)
		if n == 256 {
			var b bytes.Buffer
			must(png.Encode(&b, img))
			png256 = b.Bytes()
		}
	}

	var out bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&out, le, uint16(0))
	_ = binary.Write(&out, le, uint16(1))
	_ = binary.Write(&out, le, uint16(len(sizes)))
	offset := 6 + 16*len(sizes)
	for i, n := range sizes {
		w := byte(n)
		if n >= 256 {
			w = 0
		}
		out.WriteByte(w)
		out.WriteByte(w)
		out.WriteByte(0)
		out.WriteByte(0)
		_ = binary.Write(&out, le, uint16(1))
		_ = binary.Write(&out, le, uint16(32))
		_ = binary.Write(&out, le, uint32(len(frames[i])))
		_ = binary.Write(&out, le, uint32(offset))
		offset += len(frames[i])
	}
	for _, f := range frames {
		out.Write(f)
	}

	must(os.WriteFile(filepath.Join(dir, "tray.ico"), out.Bytes(), 0o644))
	must(os.WriteFile(filepath.Join(dir, "icon.png"), png256, 0o644))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

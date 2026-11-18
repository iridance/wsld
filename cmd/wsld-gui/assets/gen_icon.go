//go:build ignore

// 產生 wsld 圖示：全方格鯨魚（2×2 方格身體＋左上方眼＋四格 Y 形尾巴），透明背景、單色。
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

// rect 是 16×16 主格線上的方格，[x0,x1)×[y0,y1)，單位為格。
// 全部是直角方格：每個尺寸依 n/16 放大後把邊緣對齊整數像素，
// 16px 時一格＝一像素，方格間的 1 格縫隙永遠不會糊掉。
type rect struct{ x0, y0, x1, y1 int }

var (
	// 身體：2×2 大方格，間隔 1 格。
	// 尾巴：尾柄貼右下格、斜向上翹，頂端分成兩片尾鰭（四個 2×2 小方格，斜角相接）。
	fills = []rect{
		{0, 3, 4, 7}, {5, 3, 9, 7},
		{0, 8, 4, 12}, {5, 8, 9, 12},
		{10, 6, 12, 8}, {14, 6, 16, 8},
		{12, 8, 14, 10},
		{10, 10, 12, 12},
	}
	// 左上格的方形眼睛（挖空）。
	holes = []rect{{1, 4, 2, 5}}
)

// markColor 是介於淺色／深色工作列之間的藍：
// 對白底約 4.5:1、對 #1F1F1F 約 3.6:1，兩種主題的工作列都看得清楚。
var markColor = color.NRGBA{0x1f, 0x7b, 0xd0, 0xff}

// 圖形在主格線上的外框：寬 16 格、高 9 格（y 3..12）。
const gridW, markTop, markH = 16, 3, 9

func draw(n int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	// 一格只用整數像素，避免 20／24／40 這類非整數倍尺寸把方格與眼睛縮放成長方形；
	// 放不滿時置中留白。
	u := max(1, n/gridW)
	offX := (n - gridW*u) / 2
	offY := (n - markH*u) / 2
	paint := func(rs []rect, c color.NRGBA) {
		for _, r := range rs {
			for y := offY + (r.y0-markTop)*u; y < offY+(r.y1-markTop)*u; y++ {
				for x := offX + r.x0*u; x < offX+r.x1*u; x++ {
					img.SetNRGBA(x, y, c)
				}
			}
		}
	}
	paint(fills, markColor)
	paint(holes, color.NRGBA{})
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
	_ = binary.Write(&b, le, uint32(40)) // biSize
	_ = binary.Write(&b, le, int32(n))   // biWidth
	_ = binary.Write(&b, le, int32(n*2)) // biHeight：XOR + AND
	_ = binary.Write(&b, le, uint16(1))  // biPlanes
	_ = binary.Write(&b, le, uint16(32)) // biBitCount
	_ = binary.Write(&b, le, uint32(0))  // biCompression = BI_RGB
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

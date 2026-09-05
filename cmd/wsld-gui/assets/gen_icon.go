//go:build ignore

// 產生 wsld 圖示：藍色圓角方塊 + 白色容器堆疊。
//
//	go run ./cmd/wsld-gui/assets/gen_icon.go
//
// 輸出 assets/tray.ico（16/24/32/48/256 多尺寸，PNG 壓縮）與 assets/icon.png（256）。
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

func main() {
	dir := "cmd/wsld-gui/assets"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	sizes := []int{16, 24, 32, 48, 256}
	var pngs [][]byte
	for _, n := range sizes {
		var b bytes.Buffer
		if err := png.Encode(&b, draw(n)); err != nil {
			panic(err)
		}
		pngs = append(pngs, b.Bytes())
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
		_ = binary.Write(&out, le, uint32(len(pngs[i])))
		_ = binary.Write(&out, le, uint32(offset))
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		out.Write(p)
	}
	must(os.WriteFile(filepath.Join(dir, "tray.ico"), out.Bytes(), 0o644))
	must(os.WriteFile(filepath.Join(dir, "icon.png"), pngs[len(pngs)-1], 0o644))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

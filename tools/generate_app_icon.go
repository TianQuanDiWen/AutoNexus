//go:build ignore

// Run from the repository root with: go run tools/generate_app_icon.go
// Generates a deterministic multi-size ICO in memory using only the Go standard library.
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

func segmentDistance(px, py, x1, y1, x2, y2 float64) float64 {
	dx, dy := x2-x1, y2-y1
	t := ((px-x1)*dx + (py-y1)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(x1+t*dx), py-(y1+t*dy))
}

func insideDiamond(x, y float64) bool {
	poly := [][2]float64{{32, 2}, {62, 19}, {62, 45}, {32, 62}, {2, 45}, {2, 19}}
	inside := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a[1] > y) != (b[1] > y) && x < (b[0]-a[0])*(y-a[1])/(b[1]-a[1])+a[0] {
			inside = !inside
		}
	}
	return inside
}

func sample(x, y float64) color.RGBA {
	if !insideDiamond(x, y) {
		return color.RGBA{}
	}
	t := (x + (62 - y)) / 124
	base := color.RGBA{R: uint8(79 + 60*t), G: uint8(70 + 22*t), B: uint8(229 + 18*t), A: 255}
	// The white angular N is slightly heavier than a normal stroke so it remains legible at 16 px.
	d := math.Min(segmentDistance(x, y, 19, 20, 19, 44), segmentDistance(x, y, 19, 20, 45, 44))
	d = math.Min(d, segmentDistance(x, y, 45, 20, 45, 44))
	if d < 2.65 {
		return color.RGBA{255, 255, 255, 255}
	}
	if math.Hypot(x-19, y-20) < 4 || math.Hypot(x-45, y-44) < 4 {
		return color.RGBA{196, 181, 253, 255}
	}
	return base
}

func render(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	const samples = 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a uint32
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					c := sample((float64(x)+(float64(sx)+.5)/samples)*64/float64(size), (float64(y)+(float64(sy)+.5)/samples)*64/float64(size))
					r += uint32(c.R)
					g += uint32(c.G)
					b += uint32(c.B)
					a += uint32(c.A)
				}
			}
			count := uint32(samples * samples)
			img.SetRGBA(x, y, color.RGBA{uint8(r / count), uint8(g / count), uint8(b / count), uint8(a / count)})
		}
	}
	return img
}

func pngBytes(size int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, render(size))
	return b.Bytes()
}

func main() {
	assets := filepath.Join("internal", "server", "web")
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	images := make([][]byte, len(sizes))
	for i, size := range sizes {
		images[i] = pngBytes(size)
	}
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, uint16(0))
	binary.Write(&ico, binary.LittleEndian, uint16(1))
	binary.Write(&ico, binary.LittleEndian, uint16(len(sizes)))
	var offset = uint32(6 + 16*len(sizes))
	for i, size := range sizes {
		w := byte(size)
		if size == 256 {
			w = 0
		}
		ico.Write([]byte{w, w, 0, 0})
		binary.Write(&ico, binary.LittleEndian, uint16(1))
		binary.Write(&ico, binary.LittleEndian, uint16(32))
		binary.Write(&ico, binary.LittleEndian, uint32(len(images[i])))
		binary.Write(&ico, binary.LittleEndian, offset)
		offset += uint32(len(images[i]))
	}
	for _, img := range images {
		ico.Write(img)
	}
	must(os.WriteFile(filepath.Join(assets, "app-icon.ico"), ico.Bytes(), 0644))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

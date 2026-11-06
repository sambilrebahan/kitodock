// Command genicon generates the placeholder application artwork:
// desktop/build/appicon.png and desktop/build/windows/icon.ico (PNG-compressed
// entries). Pure stdlib, deterministic. Replace the output with real original
// artwork before any public release — see docs/05-risiko-dan-licensing.md §2.4
// (never ship artwork copied from another project).
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

const size = 256

// draw renders the placeholder mark: a diagonal indigo→violet gradient
// rounded square with three white "dock slot" bars.
func draw(px *image.RGBA) {
	bg1 := color.RGBA{R: 0x7c, G: 0x5c, B: 0xff, A: 0xff} // #7c5cff
	bg2 := color.RGBA{R: 0x16, G: 0x0b, B: 0x3a, A: 0xff} // deep violet
	white := color.RGBA{R: 0xf5, G: 0xf7, B: 0xff, A: 0xff}
	radius := size * 22 / 100

	// Rounded-square + diagonal gradient.
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if !insideRounded(x, y, radius) {
				continue
			}
			t := float64(x+y) / float64(2*size)
			c := lerp(bg1, bg2, t)
			px.SetRGBA(x, y, c)
		}
	}
	// Three dock slots: a tall left bar and two stacked right bars, reading as
	// an abstract "K"/dock mark.
	whiteAlpha := func(x0, y0, w, h int) {
		for y := y0; y < y0+h; y++ {
			for x := x0; x < x0+w; x++ {
				if insideRounded(x, y, radius) {
					px.SetRGBA(x, y, white)
				}
			}
		}
	}
	whiteAlpha(64, 70, 34, 116)  // vertical stem
	whiteAlpha(118, 70, 74, 46)  // upper diagonal arm
	whiteAlpha(118, 140, 74, 46) // lower diagonal arm
}

func insideRounded(x, y, r int) bool {
	corners := [4][2]int{{r, r}, {size - 1 - r, r}, {r, size - 1 - r}, {size - 1 - r, size - 1 - r}}
	for _, c := range corners {
		dx, dy := x-c[0], y-c[1]
		if (x < r || x > size-1-r) && (y < r || y > size-1-r) {
			if dx*dx+dy*dy > r*r {
				return false
			}
		}
	}
	return true
}

func lerp(a, b color.RGBA, t float64) color.RGBA {
	f := func(u, v uint8) uint8 { return uint8(float64(u) + (float64(v)-float64(u))*t) }
	return color.RGBA{R: f(a.R, b.R), G: f(a.G, b.G), B: f(a.B, b.B), A: 0xff}
}

func renderPNG(w, h int) ([]byte, error) {
	full := image.NewRGBA(image.Rect(0, 0, size, size))
	draw(full)
	if w == size {
		var buf bytes.Buffer
		if err := png.Encode(&buf, full); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			scaled.SetRGBA(x, y, full.RGBAAt(x*size/w, y*size/h))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// icoWithPNGEntries packs PNG payloads into an ICO container. Vista+ accepts
// PNG-compressed entries, including the 256px entry which must use width=0.
func icoWithPNGEntries(entries map[int][]byte) ([]byte, error) {
	var sizes []int
	for s := range entries {
		sizes = append(sizes, s)
	}
	for i := 0; i < len(sizes); i++ {
		for j := i + 1; j < len(sizes); j++ {
			if sizes[j] < sizes[i] {
				sizes[i], sizes[j] = sizes[j], sizes[i]
			}
		}
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, uint16(0))
	binary.Write(&out, binary.LittleEndian, uint16(1))
	binary.Write(&out, binary.LittleEndian, uint16(len(sizes)))
	offset := 6 + 16*len(sizes)
	for _, s := range sizes {
		dim := byte(s)
		if s >= 256 {
			dim = 0
		}
		out.WriteByte(dim)                                  // width
		out.WriteByte(dim)                                  // height
		out.WriteByte(0)                                    // palette
		out.WriteByte(0)                                    // reserved
		binary.Write(&out, binary.LittleEndian, uint16(1))  // planes
		binary.Write(&out, binary.LittleEndian, uint16(32)) // bpp
		binary.Write(&out, binary.LittleEndian, uint32(len(entries[s])))
		binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(entries[s])
	}
	for _, s := range sizes {
		out.Write(entries[s])
	}
	return out.Bytes(), nil
}

func main() {
	root := os.Args[1]
	if root == "" {
		fmt.Fprintln(os.Stderr, "usage: genicon <repo-root>")
		os.Exit(1)
	}
	appicon, err := renderPNG(size, size)
	if err != nil {
		panic(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "desktop", "build"), 0755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(root, "desktop", "build", "appicon.png"), appicon, 0644); err != nil {
		panic(err)
	}
	entries := map[int][]byte{}
	for _, s := range []int{16, 32, 48, 256} {
		b, err := renderPNG(s, s)
		if err != nil {
			panic(err)
		}
		entries[s] = b
	}
	ico, err := icoWithPNGEntries(entries)
	if err != nil {
		panic(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "desktop", "build", "windows"), 0755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(root, "desktop", "build", "windows", "icon.ico"), ico, 0644); err != nil {
		panic(err)
	}
	fmt.Println("wrote desktop/build/appicon.png and desktop/build/windows/icon.ico")
}

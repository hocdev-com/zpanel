package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

type iconImage struct {
	Width  int
	Height int
	Data   []byte
}

type point struct {
	X float64
	Y float64
}

var (
	backgroundStart = color.RGBA{R: 0x0f, G: 0x3d, B: 0x62, A: 0xff}
	backgroundEnd   = color.RGBA{R: 0x12, G: 0xb8, B: 0x86, A: 0xff}
	glyphStart      = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xf2}
	glyphEnd        = color.RGBA{R: 0xdf, G: 0xfa, B: 0xf2, A: 0xe6}
	accentColor     = color.RGBA{R: 0x8f, G: 0xf5, B: 0xd6, A: 0xff}
)

func main() {
	outPath := flag.String("out", "", "path to output ico file")
	flag.Parse()

	if *outPath == "" {
		flag.Usage()
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		panic(err)
	}

	sizes := []int{16, 24, 32, 40, 48, 64, 128, 256}
	images := make([]iconImage, 0, len(sizes))
	for _, size := range sizes {
		var pngData bytes.Buffer
		if err := png.Encode(&pngData, renderIcon(size)); err != nil {
			panic(err)
		}
		images = append(images, iconImage{
			Width:  size,
			Height: size,
			Data:   pngData.Bytes(),
		})
	}

	if err := os.WriteFile(*outPath, buildICO(images), 0o644); err != nil {
		panic(err)
	}
}

func renderIcon(size int) *image.RGBA {
	supersample := 4
	if size >= 128 {
		supersample = 2
	}

	hi := renderHighRes(size * supersample)
	if supersample == 1 {
		return hi
	}
	return downsample(hi, supersample)
}

func renderHighRes(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	zShape := []point{
		{15, 14},
		{49, 14},
		{49, 24},
		{28, 46},
		{49, 46},
		{49, 56},
		{15, 56},
		{15, 46},
		{36, 24},
		{15, 24},
	}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			ux := 64 * (float64(x) + 0.5) / float64(size)
			uy := 64 * (float64(y) + 0.5) / float64(size)
			if !insideRoundedRect(ux, uy, 4, 4, 56, 56, 16) {
				continue
			}

			bgMix := clamp((ux+uy-8)/112, 0, 1)
			img.SetRGBA(x, y, mixColor(backgroundStart, backgroundEnd, bgMix))

			if pointInPolygon(point{X: ux, Y: uy}, zShape) {
				glyphMix := clamp((ux+uy-29)/61, 0, 1)
				blendPixel(img, x, y, mixColor(glyphStart, glyphEnd, glyphMix))
			}
		}
	}

	drawStroke(img, []point{
		{44, 11},
		{48, 12},
		{52, 14},
		{55, 17},
		{57, 22},
	}, 2.2, accentColor)

	return img
}

func downsample(src *image.RGBA, factor int) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx()/factor, bounds.Dy()/factor))
	samples := uint32(factor * factor)

	for y := 0; y < dst.Bounds().Dy(); y++ {
		for x := 0; x < dst.Bounds().Dx(); x++ {
			var r, g, b, a uint32
			for sy := 0; sy < factor; sy++ {
				for sx := 0; sx < factor; sx++ {
					cr, cg, cb, ca := src.At(x*factor+sx, y*factor+sy).RGBA()
					r += cr
					g += cg
					b += cb
					a += ca
				}
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8((r / samples) >> 8),
				G: uint8((g / samples) >> 8),
				B: uint8((b / samples) >> 8),
				A: uint8((a / samples) >> 8),
			})
		}
	}

	return dst
}

func buildICO(images []iconImage) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(images)))

	offset := 6 + (16 * len(images))
	for _, img := range images {
		width := byte(img.Width)
		height := byte(img.Height)
		if img.Width >= 256 {
			width = 0
		}
		if img.Height >= 256 {
			height = 0
		}

		buf.WriteByte(width)
		buf.WriteByte(height)
		buf.WriteByte(0)
		buf.WriteByte(0)
		_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
		_ = binary.Write(&buf, binary.LittleEndian, uint16(32))
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(img.Data)))
		_ = binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(img.Data)
	}

	for _, img := range images {
		_, _ = buf.Write(img.Data)
	}

	return buf.Bytes()
}

func insideRoundedRect(x, y, left, top, width, height, radius float64) bool {
	right := left + width
	bottom := top + height
	if x < left || x > right || y < top || y > bottom {
		return false
	}

	innerLeft := left + radius
	innerRight := right - radius
	innerTop := top + radius
	innerBottom := bottom - radius
	if (x >= innerLeft && x <= innerRight) || (y >= innerTop && y <= innerBottom) {
		return true
	}

	corners := []point{
		{innerLeft, innerTop},
		{innerRight, innerTop},
		{innerLeft, innerBottom},
		{innerRight, innerBottom},
	}
	for _, corner := range corners {
		dx := x - corner.X
		dy := y - corner.Y
		if dx*dx+dy*dy <= radius*radius {
			return true
		}
	}
	return false
}

func pointInPolygon(target point, polygon []point) bool {
	inside := false
	j := len(polygon) - 1
	for i := 0; i < len(polygon); i++ {
		pi := polygon[i]
		pj := polygon[j]
		intersects := (pi.Y > target.Y) != (pj.Y > target.Y)
		if intersects {
			xCross := (pj.X-pi.X)*(target.Y-pi.Y)/(pj.Y-pi.Y) + pi.X
			if target.X < xCross {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}

func drawStroke(img *image.RGBA, points []point, radius float64, stroke color.RGBA) {
	if len(points) < 2 {
		return
	}

	const stepsPerSegment = 24
	for index := 0; index < len(points)-1; index++ {
		start := points[index]
		end := points[index+1]
		for step := 0; step <= stepsPerSegment; step++ {
			t := float64(step) / stepsPerSegment
			x := lerp(start.X, end.X, t)
			y := lerp(start.Y, end.Y, t)
			drawCircle(img, x*float64(img.Bounds().Dx())/64, y*float64(img.Bounds().Dy())/64, radius*float64(img.Bounds().Dx())/64, stroke)
		}
	}
}

func drawCircle(img *image.RGBA, cx, cy, radius float64, fill color.RGBA) {
	minX := int(math.Floor(cx - radius - 1))
	maxX := int(math.Ceil(cx + radius + 1))
	minY := int(math.Floor(cy - radius - 1))
	maxY := int(math.Ceil(cy + radius + 1))

	for y := minY; y <= maxY; y++ {
		if y < 0 || y >= img.Bounds().Dy() {
			continue
		}
		for x := minX; x <= maxX; x++ {
			if x < 0 || x >= img.Bounds().Dx() {
				continue
			}
			dx := (float64(x) + 0.5) - cx
			dy := (float64(y) + 0.5) - cy
			distance := math.Sqrt(dx*dx + dy*dy)
			if distance > radius+0.75 {
				continue
			}

			alpha := clamp(radius+0.75-distance, 0, 1)
			pixel := fill
			pixel.A = uint8(float64(fill.A) * alpha)
			blendPixel(img, x, y, pixel)
		}
	}
}

func blendPixel(img *image.RGBA, x, y int, src color.RGBA) {
	dst := img.RGBAAt(x, y)
	srcA := float64(src.A) / 255
	dstA := float64(dst.A) / 255
	outA := srcA + dstA*(1-srcA)
	if outA == 0 {
		img.SetRGBA(x, y, color.RGBA{})
		return
	}

	outR := (float64(src.R)*srcA + float64(dst.R)*dstA*(1-srcA)) / outA
	outG := (float64(src.G)*srcA + float64(dst.G)*dstA*(1-srcA)) / outA
	outB := (float64(src.B)*srcA + float64(dst.B)*dstA*(1-srcA)) / outA

	img.SetRGBA(x, y, color.RGBA{
		R: uint8(clamp(outR, 0, 255)),
		G: uint8(clamp(outG, 0, 255)),
		B: uint8(clamp(outB, 0, 255)),
		A: uint8(clamp(outA*255, 0, 255)),
	})
}

func mixColor(start, end color.RGBA, t float64) color.RGBA {
	return color.RGBA{
		R: uint8(clamp(lerp(float64(start.R), float64(end.R), t), 0, 255)),
		G: uint8(clamp(lerp(float64(start.G), float64(end.G), t), 0, 255)),
		B: uint8(clamp(lerp(float64(start.B), float64(end.B), t), 0, 255)),
		A: uint8(clamp(lerp(float64(start.A), float64(end.A), t), 0, 255)),
	}
}

func lerp(start, end, t float64) float64 {
	return start + (end-start)*t
}

func clamp(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

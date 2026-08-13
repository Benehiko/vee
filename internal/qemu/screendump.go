package qemu

import (
	"bytes"
	"fmt"
	"image"
	"strconv"
)

// maxPPMDim bounds screendump dimensions so a corrupt header can't trigger a
// huge allocation. No real display exceeds this.
const maxPPMDim = 32768

// DecodePPM decodes a binary PPM (P6) image, the format QEMU's screendump
// QMP command writes. Only what QEMU emits is supported: single-byte samples
// (maxval <= 255); plain-text P3 is rejected.
func DecodePPM(data []byte) (image.Image, error) {
	if !bytes.HasPrefix(data, []byte("P6")) {
		return nil, fmt.Errorf("not a binary PPM (P6) image")
	}
	p := ppmParser{data: data, pos: 2}

	width, err := p.nextInt()
	if err != nil {
		return nil, fmt.Errorf("PPM width: %w", err)
	}
	height, err := p.nextInt()
	if err != nil {
		return nil, fmt.Errorf("PPM height: %w", err)
	}
	maxval, err := p.nextInt()
	if err != nil {
		return nil, fmt.Errorf("PPM maxval: %w", err)
	}
	if width <= 0 || height <= 0 || width > maxPPMDim || height > maxPPMDim {
		return nil, fmt.Errorf("unreasonable PPM dimensions %dx%d", width, height)
	}
	if maxval <= 0 || maxval > 255 {
		return nil, fmt.Errorf("unsupported PPM maxval %d (want 1..255)", maxval)
	}
	// Exactly one whitespace byte separates the header from the raster.
	if p.pos >= len(data) || !isPPMSpace(data[p.pos]) {
		return nil, fmt.Errorf("malformed PPM header: missing raster separator")
	}
	p.pos++

	need := width * height * 3
	if len(data)-p.pos < need {
		return nil, fmt.Errorf("truncated PPM raster: have %d bytes, want %d", len(data)-p.pos, need)
	}

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	src := data[p.pos:]
	si := 0
	for y := 0; y < height; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+width*4]
		for x := 0; x < width; x++ {
			r, g, b := src[si], src[si+1], src[si+2]
			si += 3
			if maxval != 255 {
				r = scaleSample(r, maxval)
				g = scaleSample(g, maxval)
				b = scaleSample(b, maxval)
			}
			row[x*4+0] = r
			row[x*4+1] = g
			row[x*4+2] = b
			row[x*4+3] = 0xff
		}
	}
	return img, nil
}

// scaleSample maps a 0..maxval sample to 0..255, clamping samples that a
// malformed file put above maxval.
func scaleSample(v byte, maxval int) byte {
	s := int(v) * 255 / maxval
	if s > 255 {
		s = 255
	}
	return byte(s) //nolint:gosec // clamped to 0..255 above
}

type ppmParser struct {
	data []byte
	pos  int
}

// nextInt skips whitespace and '#' comments, then reads a decimal integer.
func (p *ppmParser) nextInt() (int, error) {
	for p.pos < len(p.data) {
		switch c := p.data[p.pos]; {
		case c == '#':
			for p.pos < len(p.data) && p.data[p.pos] != '\n' {
				p.pos++
			}
		case isPPMSpace(c):
			p.pos++
		default:
			start := p.pos
			for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
				p.pos++
			}
			if p.pos == start {
				return 0, fmt.Errorf("expected digit, found %q", c)
			}
			return strconv.Atoi(string(p.data[start:p.pos]))
		}
	}
	return 0, fmt.Errorf("unexpected end of header")
}

func isPPMSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

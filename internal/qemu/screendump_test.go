package qemu

import (
	"image/color"
	"testing"
)

// buildP6 assembles a P6 PPM with the given header fields and raster bytes.
func buildP6(header string, raster []byte) []byte {
	return append([]byte(header), raster...)
}

func TestDecodePPM(t *testing.T) {
	// 2x2: red, green / blue, white.
	raster := []byte{
		255, 0, 0, 0, 255, 0,
		0, 0, 255, 255, 255, 255,
	}
	img, err := DecodePPM(buildP6("P6\n2 2\n255\n", raster))
	if err != nil {
		t.Fatalf("DecodePPM: %v", err)
	}
	if got := img.Bounds(); got.Dx() != 2 || got.Dy() != 2 {
		t.Fatalf("bounds = %v, want 2x2", got)
	}
	want := map[[2]int]color.NRGBA{
		{0, 0}: {R: 255, A: 255},
		{1, 0}: {G: 255, A: 255},
		{0, 1}: {B: 255, A: 255},
		{1, 1}: {R: 255, G: 255, B: 255, A: 255},
	}
	for pt, w := range want {
		if got := img.At(pt[0], pt[1]); got != w {
			t.Errorf("pixel (%d,%d) = %v, want %v", pt[0], pt[1], got, w)
		}
	}
}

func TestDecodePPMHeaderVariants(t *testing.T) {
	raster := []byte{1, 2, 3}
	// Comments and mixed whitespace in the header are legal PPM.
	img, err := DecodePPM(buildP6("P6 # comment\n# another\n 1\t1 # trailing\n255 ", raster))
	if err != nil {
		t.Fatalf("DecodePPM with comments: %v", err)
	}
	if got := img.At(0, 0); got != (color.NRGBA{R: 1, G: 2, B: 3, A: 255}) {
		t.Errorf("pixel = %v", got)
	}
}

func TestDecodePPMMaxvalScaling(t *testing.T) {
	// maxval 85: sample 85 must scale to 255, 17 to 51.
	img, err := DecodePPM(buildP6("P6\n1 1\n85\n", []byte{85, 17, 0}))
	if err != nil {
		t.Fatalf("DecodePPM: %v", err)
	}
	if got := img.At(0, 0); got != (color.NRGBA{R: 255, G: 51, B: 0, A: 255}) {
		t.Errorf("pixel = %v, want scaled {255 51 0 255}", got)
	}
}

func TestDecodePPMErrors(t *testing.T) {
	cases := map[string][]byte{
		"empty":            {},
		"wrong magic":      buildP6("P3\n1 1\n255\n", []byte{0, 0, 0}),
		"missing height":   []byte("P6\n2\n"),
		"zero width":       buildP6("P6\n0 1\n255\n", nil),
		"huge dims":        []byte("P6\n99999 99999\n255\n"),
		"maxval too big":   buildP6("P6\n1 1\n65535\n", []byte{0, 0, 0, 0, 0, 0}),
		"truncated raster": buildP6("P6\n2 2\n255\n", []byte{1, 2, 3}),
		"no separator":     []byte("P6\n1 1\n255"),
	}
	for name, data := range cases {
		if _, err := DecodePPM(data); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestUniformImage(t *testing.T) {
	black := make([]byte, 2*2*3)
	blackImg, err := DecodePPM(buildP6("P6\n2 2\n255\n", black))
	if err != nil {
		t.Fatal(err)
	}
	if !UniformImage(blackImg) {
		t.Error("all-black frame not reported uniform")
	}

	mixed := []byte{
		255, 0, 0, 0, 255, 0,
		0, 0, 255, 255, 255, 255,
	}
	mixedImg, err := DecodePPM(buildP6("P6\n2 2\n255\n", mixed))
	if err != nil {
		t.Fatal(err)
	}
	if UniformImage(mixedImg) {
		t.Error("multi-color frame reported uniform")
	}

	// A single off pixel in an otherwise solid frame must break uniformity.
	almost := make([]byte, 3*3*3)
	almost[len(almost)-1] = 1
	almostImg, err := DecodePPM(buildP6("P6\n3 3\n255\n", almost))
	if err != nil {
		t.Fatal(err)
	}
	if UniformImage(almostImg) {
		t.Error("frame with one differing pixel reported uniform")
	}
}

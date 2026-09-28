package lock

import (
	"image"
	"testing"
	"time"
)

func TestFormatRemaining(t *testing.T) {
	cases := map[time.Duration]string{
		5 * time.Minute:                    "05:00",
		4*time.Minute + 59*time.Second + 1: "05:00", // rounds up
		300 * time.Millisecond:             "00:01", // final second isn't 00:00
		0:                                  "00:00",
		-time.Second:                       "00:00",
		time.Hour + 2*time.Minute + 3*time.Second: "1:02:03",
	}
	for d, want := range cases {
		if got := formatRemaining(d); got != want {
			t.Errorf("formatRemaining(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestCoverRect(t *testing.T) {
	// 400x100 source onto a square: crop the sides to the centred 100x100.
	if got := coverRect(image.Rect(0, 0, 400, 100), 50, 50); got != image.Rect(150, 0, 250, 100) {
		t.Errorf("wide: %v", got)
	}
	// 100x400 source onto a square: crop top/bottom.
	if got := coverRect(image.Rect(0, 0, 100, 400), 50, 50); got != image.Rect(0, 150, 100, 250) {
		t.Errorf("tall: %v", got)
	}
}

func TestRenderXRGBDrawsCountdown(t *testing.T) {
	const w, h = 320, 200
	const bg = 0x1a3d2e
	r, err := newFrameRenderer(w, h, bg, nil)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, w*h*4)
	r.renderXRGB(buf, w*4, "Screen locked", 90*time.Second)

	// Byte order is B,G,R,X: a background pixel in the corner…
	if buf[0] != 0x2e || buf[1] != 0x3d || buf[2] != 0x1a || buf[3] != 0xff {
		t.Fatalf("corner pixel = % x, want background 2e 3d 1a ff", buf[:4])
	}
	// …and white text somewhere in the middle band (the countdown).
	white := 0
	for y := h / 3; y < h*2/3; y++ {
		for x := 0; x < w; x++ {
			p := buf[(y*w+x)*4:]
			if p[0] > 0xf0 && p[1] > 0xf0 && p[2] > 0xf0 {
				white++
			}
		}
	}
	if white < 50 {
		t.Fatalf("only %d white pixels in the centre band; countdown not drawn", white)
	}
}

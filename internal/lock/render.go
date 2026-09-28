package lock

import (
	"fmt"
	"image"
	"image/color"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// frameRenderer draws lock-screen frames in software — the message and a live
// countdown over a background — for backends with no server-side text (Wayland
// hands us a bare shm buffer). The font is the embedded Go Bold, so it needs no
// system fonts.
type frameRenderer struct {
	base     *image.RGBA // background, rendered once per size
	scratch  *image.RGBA // base + text, reused every frame
	msgFace  font.Face
	timeFace font.Face
}

var (
	goBoldOnce sync.Once
	goBold     *opentype.Font
	goBoldErr  error
)

func loadGoBold() (*opentype.Font, error) {
	goBoldOnce.Do(func() { goBold, goBoldErr = opentype.Parse(gobold.TTF) })
	return goBold, goBoldErr
}

// newFrameRenderer prepares a renderer for a width×height surface. bgImage, if
// non-nil, is scaled to cover the surface; otherwise bgColor (0xRRGGBB) fills it.
func newFrameRenderer(width, height int, bgColor uint32, bgImage image.Image) (*frameRenderer, error) {
	f, err := loadGoBold()
	if err != nil {
		return nil, err
	}
	// Size the text off the shorter side so it stays proportionate on portrait
	// and ultrawide outputs alike.
	short := min(width, height)
	msgFace, err := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(max(short/22, 12)), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	timeFace, err := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(max(short/7, 24)), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}

	rect := image.Rect(0, 0, width, height)
	base := image.NewRGBA(rect)
	if bgImage != nil {
		xdraw.ApproxBiLinear.Scale(base, rect, bgImage, coverRect(bgImage.Bounds(), width, height), xdraw.Src, nil)
	} else {
		c := color.RGBA{uint8(bgColor >> 16), uint8(bgColor >> 8), uint8(bgColor), 0xff}
		xdraw.Draw(base, rect, &image.Uniform{c}, image.Point{}, xdraw.Src)
	}
	return &frameRenderer{base: base, scratch: image.NewRGBA(rect), msgFace: msgFace, timeFace: timeFace}, nil
}

// coverRect returns the centred region of src that, scaled to w×h, covers it
// without distortion (CSS background-size: cover).
func coverRect(src image.Rectangle, w, h int) image.Rectangle {
	sw, sh := src.Dx(), src.Dy()
	if sw*h > sh*w { // source is wider: crop the sides
		cw := sh * w / h
		x0 := src.Min.X + (sw-cw)/2
		return image.Rect(x0, src.Min.Y, x0+cw, src.Max.Y)
	}
	ch := sw * h / w
	y0 := src.Min.Y + (sh-ch)/2
	return image.Rect(src.Min.X, y0, src.Max.X, y0+ch)
}

// formatRemaining renders a countdown as MM:SS (H:MM:SS past an hour),
// rounding up so it reads 00:01 — not 00:00 — during the final second.
func formatRemaining(d time.Duration) string {
	secs := int((d + time.Second - 1) / time.Second)
	if secs < 0 {
		secs = 0
	}
	if secs >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", secs/3600, secs/60%60, secs%60)
	}
	return fmt.Sprintf("%02d:%02d", secs/60, secs%60)
}

// renderXRGB draws a frame and writes it to dst as little-endian XRGB8888 (the
// wl_shm format: bytes B, G, R, X) with the given stride.
func (r *frameRenderer) renderXRGB(dst []byte, stride int, message string, remaining time.Duration) {
	img := r.scratch
	copy(img.Pix, r.base.Pix)

	b := img.Bounds()
	timeText := formatRemaining(remaining)
	// Countdown just above centre, the message and a caption below it.
	timeY := b.Dy()/2 + r.timeFace.Metrics().Ascent.Ceil()/3
	lineGap := r.msgFace.Metrics().Height.Ceil()
	drawCentered(img, r.timeFace, timeText, timeY)
	drawCentered(img, r.msgFace, message, timeY+lineGap*2)
	drawCentered(img, r.msgFace, "Unlocks automatically", timeY+lineGap*3+lineGap/3)

	w, h := b.Dx(), b.Dy()
	for y := 0; y < h; y++ {
		src := img.Pix[y*img.Stride : y*img.Stride+w*4]
		out := dst[y*stride : y*stride+w*4]
		for x := 0; x < w*4; x += 4 {
			out[x], out[x+1], out[x+2], out[x+3] = src[x+2], src[x+1], src[x], 0xff
		}
	}
}

// drawCentered draws s horizontally centred with its baseline at y, with a soft
// shadow so it stays legible over a busy background image.
func drawCentered(img *image.RGBA, face font.Face, s string, y int) {
	d := &font.Drawer{Dst: img, Face: face}
	width := d.MeasureString(s)
	x := (fixed.I(img.Bounds().Dx()) - width) / 2
	off := fixed.I(max(face.Metrics().Height.Ceil()/24, 1))

	d.Src = image.NewUniform(color.RGBA{0, 0, 0, 0x90})
	d.Dot = fixed.Point26_6{X: x + off, Y: fixed.I(y) + off}
	d.DrawString(s)

	d.Src = image.White
	d.Dot = fixed.Point26_6{X: x, Y: fixed.I(y)}
	d.DrawString(s)
}

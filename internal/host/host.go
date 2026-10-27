// Package host runs the game on Ebiten.
//
// The original program is written in a blocking style: its main loop, menus and cutscenes
// spin until something happens. The game runs on its own goroutine in lockstep with Ebiten:
// each Ebiten tick hands control to the game goroutine, which runs until it calls Yield (at a
// page flip, an input poll or a busy wait), then Ebiten draws the screen the game left
// behind. Only one of the two runs at a time, so the game's state needs no locks.
package host

import (
	"image"
	"image/color"
	"math"
	"os"
	"sync/atomic"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

const (
	ScreenWidth  = 320
	ScreenHeight = 200
	// TicksPerSecond is the host's tick rate (the VGA's 70 Hz vertical retrace).
	TicksPerSecond = 70
	// vgaLevels is the number of levels of a 6-bit VGA DAC colour component.
	vgaLevels = 64
	// crtStretch is how much taller than wide a 320x200 pixel showed on a 4:3 monitor.
	crtStretch = 1.2
	// fullShade is a Frame.Shade value that leaves a pixel at its colour.
	fullShade = math.MaxUint8
	// MaxScreenWidth and MaxScreenHeight bound the screen the game may widen or heighten
	// to the window's shape (FitWidth, FitHeight); a wider window shows black bars.
	MaxScreenWidth  = 736
	MaxScreenHeight = 736
)

// SquarePixels shows the pixels square instead of at the CRT's shape (taller than wide, the
// picture 4:3), Nuvie's non_square_pixels off.
var SquarePixels bool

// pixelAspect is how much taller than wide a pixel is shown.
func pixelAspect() float64 {
	if SquarePixels {
		return 1
	}
	return crtStretch
}

// FitWidth and FitHeight are the size of a screen that fills the window at the CRT's pixel
// shape: 320x200 widened or heightened to the window's aspect ratio. Frames of that size
// fill the window; others, like the intro's 320x200, are shown whole at the CRT's shape.
var FitWidth, FitHeight = ScreenWidth, ScreenHeight

// fitCovers says that a frame of the fitted size covers the window: it is not cut short
// by the bounds.
var fitCovers bool

// Frame is what the host shows: a paletted picture (top row first), its palette of 6-bit
// VGA values, and the mouse pointer.
type Frame struct {
	Pixels []byte
	// Width and Height are the picture's size; zero means 320x200.
	Width, Height int
	Palette       *[768]byte
	// Pointer is drawn over the frame at (PointerX, PointerY) when PointerShown; colour 0 is
	// transparent.
	Pointer                     []byte
	PointerWidth, PointerHeight int
	PointerHotX, PointerHotY    int
	PointerX, PointerY          int
	PointerShown                bool
	// Shade, when not nil, dims each pixel: 0 is black, 255 its colour (smooth lighting).
	Shade []byte
}

// ShowFrame returns the frame to present. Set by the game.
var ShowFrame func() *Frame

var (
	// run is the game goroutine's hand-over: a new game gets its own, so a game a test left
	// behind stays parked instead of taking the new one's ticks
	run      = newHandOver()
	finished atomic.Bool

	// Ticks counts host ticks since start.
	Ticks uint64

	waitCarry time.Duration // the part of a Wait shorter than a tick, carried over
)

// Wait lets d pass on the host clock, carrying what is shorter than a tick over to the next
// Wait, so many short waits add up to the right time.
func Wait(d time.Duration) {
	waitCarry += d
	for waitCarry >= time.Second/TicksPerSecond {
		waitCarry -= time.Second / TicksPerSecond
		Yield()
	}
}

// Yield hands control back to Ebiten until its next tick. Only the game goroutine calls it.
func Yield() {
	r := run
	r.yielded <- struct{}{}
	<-r.tick
}

type handOver struct{ tick, yielded chan struct{} }

func newHandOver() *handOver {
	return &handOver{make(chan struct{}), make(chan struct{})}
}

func startGame(gameMain func()) {
	run = newHandOver()
	r := run
	go func() {
		<-r.tick
		defer func() {
			finished.Store(true)
			r.yielded <- struct{}{}
		}()
		gameMain()
	}()
}

// Run starts Ebiten with the game's entry point running on its own goroutine.
func Run(title string, scale int, gameMain func()) error {
	ebiten.SetWindowTitle(title)
	ebiten.SetWindowSize(ScreenWidth*scale, int(ScreenHeight*crtStretch)*scale)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetTPS(TicksPerSecond)
	startGame(gameMain)
	ebiten.SetFullscreen(os.Getenv("GOPLAY_FULLSCREEN") == "1") // the goplay launcher asks for fullscreen
	return ebiten.RunGame(&game{})
}

type game struct {
	image *ebiten.Image
	rgba  *image.RGBA
	// the picture's size and placement on the window, for mapping the pointer
	width, height                    int
	offsetX, offsetY, scaleX, scaleY float64
}

// step runs the game goroutine for one tick; false once it has ended.
func step() bool {
	if finished.Load() {
		return false
	}
	Ticks++
	runTimers()
	run.tick <- struct{}{}
	<-run.yielded
	return !finished.Load()
}

func (g *game) Update() error {
	pollInput(g)
	if !step() {
		return ebiten.Termination
	}
	return nil
}

func (g *game) Draw(screen *ebiten.Image) {
	screen.Fill(color.Black)
	if ShowFrame == nil {
		return
	}
	frame := ShowFrame()
	if frame == nil {
		return
	}
	width, height := frame.Width, frame.Height
	if width == 0 {
		width, height = ScreenWidth, ScreenHeight
	}
	if g.image == nil || width != g.width || height != g.height {
		g.width, g.height = width, height
		g.image = ebiten.NewImage(width, height)
		g.rgba = image.NewRGBA(image.Rect(0, 0, width, height))
	}
	var colours [256][4]byte
	for i := range colours {
		for c := 0; c < 3; c++ {
			level := int(frame.Palette[i*3+c]) % vgaLevels
			colours[i][c] = byte(level * math.MaxUint8 / (vgaLevels - 1))
		}
		colours[i][3] = math.MaxUint8 // opaque
	}
	out := g.rgba.Pix
	for i, c := range frame.Pixels[:width*height] {
		copy(out[i*4:i*4+4], colours[c][:])
	}
	if frame.Shade != nil {
		for i, shade := range frame.Shade[:width*height] {
			for c := i * 4; c < i*4+3; c++ {
				out[c] = byte(int(out[c]) * int(shade) / fullShade)
			}
		}
	}
	if hardwareCursor {
		g.showCursor(frame, &colours)
	} else if frame.PointerShown {
		for y := 0; y < frame.PointerHeight; y++ {
			sy := frame.PointerY - frame.PointerHotY + y
			for x := 0; x < frame.PointerWidth; x++ {
				sx := frame.PointerX - frame.PointerHotX + x
				c := frame.Pointer[y*frame.PointerWidth+x]
				if c != 0 && sx >= 0 && sx < width && sy >= 0 && sy < height {
					copy(out[(sy*width+sx)*4:], colours[c][:])
				}
			}
		}
	}
	g.image.WritePixels(out)

	// Fit the picture, its pixels at the CRT's shape, in the window: a picture of the fitted
	// size covers it (rounding up leaves less than a pixel out), others are shown whole.
	windowW, windowH := float64(screen.Bounds().Dx()), float64(screen.Bounds().Dy())
	shownW, shownH := float64(width), float64(height)*pixelAspect()
	k := min(windowW/shownW, windowH/shownH)
	if fitCovers && width == FitWidth && height == FitHeight {
		k = max(windowW/shownW, windowH/shownH)
	}
	g.scaleX, g.scaleY = k, k*pixelAspect()
	g.offsetX, g.offsetY = (windowW-shownW*k)/2, (windowH-shownH*k)/2
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(g.scaleX, g.scaleY)
	op.GeoM.Translate(g.offsetX, g.offsetY)
	screen.DrawImage(g.image, op)
	drawCursor(screen)
}

func (g *game) Layout(outsideWidth, outsideHeight int) (int, int) {
	s := ebiten.Monitor().DeviceScaleFactor()
	w, h := int(float64(outsideWidth)*s), int(float64(outsideHeight)*s)
	Fit(w, h)
	return w, h
}

// Fit sizes the screen (FitWidth, FitHeight) for a window of windowW x windowH pixels.
func Fit(windowW, windowH int) {
	FitWidth, FitHeight, fitCovers = fitScreen(windowW, windowH)
}

// fitScreen returns the screen size, in even pixels, whose CRT-shaped pixels have the
// window's aspect ratio: 200 high and wider for a window wider than 4:3, else 320 wide and
// taller, within the bounds; covers is false when the bounds cut it short.
func fitScreen(windowW, windowH int) (width, height int, covers bool) {
	if windowW <= 0 || windowH <= 0 {
		return ScreenWidth, ScreenHeight, false
	}
	even := func(v float64) int { return int(math.Ceil(v/2)) * 2 }
	aspect := float64(windowW) / float64(windowH)
	if aspect*ScreenHeight*pixelAspect() >= ScreenWidth {
		width = even(ScreenHeight * pixelAspect() * aspect)
		return min(width, MaxScreenWidth), ScreenHeight, width <= MaxScreenWidth
	}
	height = even(ScreenWidth / pixelAspect() / aspect)
	return ScreenWidth, min(height, MaxScreenHeight), height <= MaxScreenHeight
}

// StartHeadless starts the game goroutine without a window, for tests; Step then runs it one
// host tick at a time.
func StartHeadless(gameMain func()) {
	finished.Store(false) // a test may run one game after another
	KeyQueue = KeyQueue[:0]
	Ticks, waitCarry = 0, 0 // a new game starts on a fresh host clock
	startGame(gameMain)
}

// Step runs one host tick of a headless game; false once the game has ended.
func Step() bool { return step() }

// Timers are callbacks run every host tick before the game runs, as the DOS timer interrupt
// ran them.
var timers []func()

// AddTimer registers a callback run once per host tick.
func AddTimer(f func()) { timers = append(timers, f) }

func runTimers() {
	for _, f := range timers {
		f()
	}
}

// showCursor makes the frame's pointer the hardware cursor, scaled as the picture is.
func (g *game) showCursor(frame *Frame, colours *[256][4]byte) {
	if !frame.PointerShown || g.scaleX == 0 {
		setCursor(nil, 0, 0)
		return
	}
	// the cursor image is in CSS pixels, the picture's scale in device pixels
	dpr := ebiten.Monitor().DeviceScaleFactor()
	sx, sy := g.scaleX/dpr, g.scaleY/dpr
	w, h := int(float64(frame.PointerWidth)*sx), int(float64(frame.PointerHeight)*sy)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if c := frame.Pointer[int(float64(y)/sy)*frame.PointerWidth+int(float64(x)/sx)]; c != 0 {
				copy(img.Pix[img.PixOffset(x, y):], colours[c][:])
			}
		}
	}
	setCursor(img, int(float64(frame.PointerHotX)*sx), int(float64(frame.PointerHotY)*sy))
}

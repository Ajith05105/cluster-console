// Package scene draws the fake camera pictures used across the project.
//
// There is no real camera. Instead we draw a simple grey "room" in code:
// a wall, a floor and a dark door. That empty room is the REFERENCE picture.
// A camera FRAME is the same room, sometimes with a bright box standing in it
// (think of it as a person walking past).
//
// Two programs use this package:
//   - camera-ingest compares every frame it receives against the reference
//     picture to decide whether something moved.
//   - the load generator (Phase 2) sends these frames as if it were a camera.
//
// Because both sides draw from the same code, the pictures always match and
// no image files need to be shipped around.
package scene

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
)

// Size of every picture, in pixels. Small on purpose: a frame is only a few
// kilobytes once it is saved as a JPEG.
const (
	Width  = 320
	Height = 240
)

// FrameCount is how many different frames exist. Frame 0 is the empty room
// (no motion). Frames 1, 2 and 3 have the bright box in three different
// places (motion).
const FrameCount = 4

// Reference draws the empty room. It gives back exactly the same picture
// every time it is called.
func Reference() *image.Gray {
	// image.Gray is a black-and-white picture: one brightness number
	// (0 = black, 255 = white) per pixel.
	img := image.NewGray(image.Rect(0, 0, Width, Height))

	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			// The wall gets slowly brighter from left to right.
			brightness := 90 + x*60/Width
			// The bottom quarter of the picture is a darker floor.
			if y >= Height*3/4 {
				brightness = 60
			}
			img.SetGray(x, y, color.Gray{Y: uint8(brightness)})
		}
	}

	// A dark door on the right-hand side.
	fill(img, image.Rect(230, 70, 280, 180), 40)
	return img
}

// Frame draws camera frame number i. Any number is accepted; it wraps around,
// so frame 4 is the same as frame 0, frame 5 the same as frame 1, and so on.
func Frame(i int) *image.Gray {
	img := Reference()

	position := i % FrameCount
	if position < 0 {
		position += FrameCount
	}
	if position == 0 {
		// Frame 0 is the empty room: nothing has moved.
		return img
	}

	// Frames 1 to 3: a bright box, 50 pixels wide and 110 tall, placed
	// further to the right for each frame number.
	left := 20 + (position-1)*70
	fill(img, image.Rect(left, 90, left+50, 200), 230)
	return img
}

// JPEG turns a picture into JPEG file bytes, ready to send over HTTP.
func JPEG(img image.Image) []byte {
	var buf bytes.Buffer
	// Quality 75 is a normal "good enough" JPEG setting. Encoding into a
	// memory buffer cannot fail for a valid picture, so the error is ignored.
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75})
	return buf.Bytes()
}

// Frames returns all the frames as ready-made JPEGs, in order. Callers make
// this list once at start-up and then reuse it, so no drawing or encoding
// happens while load is running.
func Frames() [][]byte {
	frames := make([][]byte, FrameCount)
	for i := range frames {
		frames[i] = JPEG(Frame(i))
	}
	return frames
}

// fill paints a solid rectangle of one brightness onto the picture.
func fill(img *image.Gray, area image.Rectangle, brightness uint8) {
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			img.SetGray(x, y, color.Gray{Y: brightness})
		}
	}
}

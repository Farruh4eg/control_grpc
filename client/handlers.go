package main

import (
	"io"
	"log"
	"sync"
	"time"

	pb "control_grpc/gen/proto"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// mouseOverlay is a transparent widget that captures mouse events and sends them over gRPC.
type mouseOverlay struct {
	widget.BaseWidget
	stream pb.RemoteControlService_GetFeedClient
}

// newMouseOverlay creates a new instance of mouseOverlay.
func newMouseOverlay(stream pb.RemoteControlService_GetFeedClient) *mouseOverlay {
	mo := &mouseOverlay{stream: stream}
	mo.ExtendBaseWidget(mo)
	return mo
}

// CreateRenderer returns an empty renderer as the widget is transparent.
func (mo *mouseOverlay) CreateRenderer() fyne.WidgetRenderer {
	// The widget is transparent and doesn't render anything itself.
	// It relies on its size to capture mouse events over the video feed.
	empty := container.NewWithoutLayout() // Use a simple container
	return widget.NewSimpleRenderer(empty)
}

// scaleCoordinates scales the mouse position from Fyne's coordinate system
// to the target resolution (e.g., 1920x1080).
func (mo *mouseOverlay) scaleCoordinates(pos fyne.Position) (float32, float32) {
	sz := mo.Size()
	if sz.Width == 0 || sz.Height == 0 {
		return 0, 0 // Avoid division by zero
	}
	// Assuming the server expects coordinates based on a 1920x1080 resolution
	targetWidth := float32(1920.0)
	targetHeight := float32(1080.0)

	scaleX := targetWidth / sz.Width
	scaleY := targetHeight / sz.Height
	return pos.X * scaleX, pos.Y * scaleY
}

// sendMouseEvent formats and sends a mouse event message via the gRPC stream.
// It uses the global `mouseEvents` channel for non-blocking send.
func (mo *mouseOverlay) sendMouseEvent(eventType, btn string, pos fyne.Position) {
	sx, sy := mo.scaleCoordinates(pos)
	req := &pb.FeedRequest{
		Message:        "mouse_event",
		MouseX:         int32(sx),
		MouseY:         int32(sy),
		MouseBtn:       btn, // Button info might need to be tracked if not directly available
		MouseEventType: eventType,
		ClientWidth:    1920, // Fixed client dimensions for now
		ClientHeight:   1080,
		Timestamp:      time.Now().UnixNano(),
	}

	// Send to the channel instead of blocking stream.Send()
	select {
	case mouseEvents <- req:
		// log.Printf("Mouse event queued: %s, Btn: %s, X: %d, Y: %d", eventType, btn, int(sx), int(sy)) // Verbose
	default:
		log.Println("Mouse event dropped (channel full)")
	}
}

// MouseIn is called when the cursor enters the widget's area.
func (mo *mouseOverlay) MouseIn(ev *desktop.MouseEvent) {
	// The 'btn' parameter is empty as MouseIn doesn't involve a button press.
	mo.sendMouseEvent("in", "", ev.Position)
}

// MouseMoved is called when the cursor moves over the widget.
func (mo *mouseOverlay) MouseMoved(ev *desktop.MouseEvent) {
	// The 'btn' parameter is empty for mouse move events.
	// If you need to send button state during drag, you'd need to track it.
	mo.sendMouseEvent("move", "", ev.Position)
}

// MouseOut is called when the cursor leaves the widget's area.
func (mo *mouseOverlay) MouseOut() {
	// Typically, a "out" event might not need coordinates or button.
	// Adjust if your server expects specific data for "out" events.
	// mo.sendMouseEvent("out", "", fyne.Position{}) // Example, if needed
}

// Tapped is Fyne's equivalent of a click (mouse down and up on the element).
func (mo *mouseOverlay) Tapped(ev *fyne.PointEvent) {
	// For primary button tap (usually left click)
	// log.Printf("Mouse Tapped (Primary) at %v", ev.Position) // Debug
	mo.sendMouseEvent("down", "left", ev.Position) // Send mouse down
	mo.sendMouseEvent("up", "left", ev.Position)   // Send mouse up
}

// TappedSecondary is for secondary button taps (e.g., right-click).
func (mo *mouseOverlay) TappedSecondary(ev *fyne.PointEvent) {
	// log.Printf("Mouse Tapped (Secondary) at %v", ev.Position) // Debug
	mo.sendMouseEvent("down", "right", ev.Position) // Send mouse down for right button
	mo.sendMouseEvent("up", "right", ev.Position)   // Send mouse up for right button
}

// MouseDown is part of the desktop.Hoverable interface, but mouseOverlay uses Tapped/TappedSecondary.
// If you need separate down/up events without the full tap, you'd implement desktop.Mouseable.
// For simplicity, we're using Tapped which implies a quick down and up.
// If your server needs distinct "mousedown" and "mouseup" events, you might need a more complex setup
// or adjust the server to interpret a quick sequence of "down" then "up" from Tapped.

// MouseTracker can be used if more complex button state tracking is needed across events.
// Currently, Tapped/TappedSecondary provide basic click info.
type MouseTracker struct {
	mu       sync.Mutex
	mouseBtn string // "left", "right", "middle", ""
}

func (mt *MouseTracker) setButton(btn string) {
	mt.mu.Lock()
	mt.mouseBtn = btn
	mt.mu.Unlock()
}

func (mt *MouseTracker) getButton() string {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	return mt.mouseBtn
}

// forwardVideoFeed receives video frames from the gRPC stream and writes them to the FFmpeg input.
func forwardVideoFeed(stream pb.RemoteControlService_GetFeedClient, ffmpegInput io.Writer) {
	defer func() {
		log.Println("forwardVideoFeed goroutine stopped.")
		if closer, ok := ffmpegInput.(io.Closer); ok {
			log.Println("Closing ffmpegInput in forwardVideoFeed.")
			closer.Close() // Close the pipe writer to signal EOF to FFmpeg
		}
	}()

	for {
		frame, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				log.Println("Video stream EOF from server.")
			} else {
				log.Printf("Error receiving video frame from server: %v", err)
			}
			return // Stream finished or error
		}

		if frame.Data == nil {
			log.Println("Received nil frame data from server.")
			continue
		}

		// log.Printf("Received frame from server, size: %d bytes", len(frame.Data)) // Verbose
		_, writeErr := ffmpegInput.Write(frame.Data)
		if writeErr != nil {
			log.Printf("Error writing video frame to FFmpeg input: %v", writeErr)
			// If ffmpegInput is a pipe and the reader side closes, this will error.
			return // Stop if we can't write
		}
	}
}

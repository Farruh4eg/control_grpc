package main

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"log"
	"sync"
	"time"

	pb "control_grpc/gen/proto"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop" // Required for Mouseable and Hoverable interfaces
	"fyne.io/fyne/v2/widget"
)

// mouseOverlay is a transparent widget that captures mouse events and sends them over gRPC.
// It now implements desktop.Mouseable and desktop.Hoverable.
type mouseOverlay struct {
	widget.BaseWidget
	stream        pb.RemoteControlService_GetFeedClient
	mouseBtnState string     // Tracks current mouse button state: "left", "right", "middle", or "" if none
	mu            sync.Mutex // For protecting mouseBtnState
}

// newMouseOverlay creates a new instance of mouseOverlay.
func newMouseOverlay(stream pb.RemoteControlService_GetFeedClient) *mouseOverlay {
	mo := &mouseOverlay{stream: stream}
	mo.ExtendBaseWidget(mo) // Important for Fyne to recognize this as a widget
	return mo
}

// CreateRenderer returns an empty renderer as the widget is transparent.
func (mo *mouseOverlay) CreateRenderer() fyne.WidgetRenderer {
	// The widget is transparent and doesn't render anything itself.
	// It relies on its size to capture mouse events over the video feed.
	// Using NewSimpleRenderer with an empty container is a common way for invisible event catchers.
	return widget.NewSimpleRenderer(container.NewWithoutLayout())
}

// scaleCoordinates scales the mouse position from Fyne's coordinate system
// to the target resolution (e.g., 1920x1080).
func (mo *mouseOverlay) scaleCoordinates(pos fyne.Position) (float32, float32) {
	sz := mo.Size()
	if sz.Width == 0 || sz.Height == 0 {
		return 0, 0 // Avoid division by zero if widget hasn't been sized yet
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
		MouseBtn:       btn,       // Current button state or button involved in event
		MouseEventType: eventType, // "down", "up", "move", "in", "out"
		ClientWidth:    1920,      // Fixed client dimensions for now
		ClientHeight:   1080,
		Timestamp:      time.Now().UnixNano(),
	}

	// Send to the channel instead of blocking stream.Send()
	select {
	case mouseEvents <- req:
		// log.Printf("Mouse event queued: Type: %s, Btn: %s, Pos: (%.0f, %.0f)", eventType, btn, sx, sy) // Verbose
	default:
		log.Println("Mouse event dropped (channel full)")
	}
}

// --- desktop.Hoverable interface implementation ---

// MouseIn is called when the cursor enters the widget's area.
func (mo *mouseOverlay) MouseIn(_ *desktop.MouseEvent) { // Event arg often not needed for MouseIn itself
	// log.Println("Mouse In") // Debug
	// For MouseIn, button state is typically not relevant unless a drag-enter.
	// Sending current button state if needed, or "" if not.
	mo.mu.Lock()
	currentBtn := mo.mouseBtnState
	mo.mu.Unlock()
	mo.sendMouseEvent("in", currentBtn, fyne.Position{}) // Position might not be critical for "in"
}

// MouseMoved is called when the cursor moves over the widget.
func (mo *mouseOverlay) MouseMoved(ev *desktop.MouseEvent) {
	// log.Printf("Mouse Moved to %v", ev.Position) // Debug
	// Send the current button state if a button is being held (dragging)
	mo.mu.Lock()
	currentBtn := mo.mouseBtnState
	mo.mu.Unlock()
	mo.sendMouseEvent("move", currentBtn, ev.Position)
}

// MouseOut is called when the cursor leaves the widget's area.
func (mo *mouseOverlay) MouseOut() {
	// log.Println("Mouse Out") // Debug
	// Similar to MouseIn, button state might be relevant for drag-leave.
	mo.mu.Lock()
	currentBtn := mo.mouseBtnState
	mo.mu.Unlock()
	mo.sendMouseEvent("out", currentBtn, fyne.Position{}) // Position might not be critical for "out"
}

// --- desktop.Mouseable interface implementation ---

// MouseDown is called when a mouse button is pressed down over the widget.
func (mo *mouseOverlay) MouseDown(ev *desktop.MouseEvent) {
	var btnStr string
	switch ev.Button {
	case desktop.MouseButtonPrimary:
		btnStr = "left"
	case desktop.MouseButtonSecondary:
		btnStr = "right"
	case desktop.MouseButtonTertiary:
		btnStr = "middle"
	default:
		btnStr = "unknown" // Should not happen for standard buttons
	}
	// log.Printf("Mouse Down: %s at %v", btnStr, ev.Position) // Debug

	mo.mu.Lock()
	mo.mouseBtnState = btnStr // Set the currently pressed button
	mo.mu.Unlock()

	mo.sendMouseEvent("down", btnStr, ev.Position)
}

// MouseUp is called when a mouse button is released over the widget.
func (mo *mouseOverlay) MouseUp(ev *desktop.MouseEvent) {
	var btnStr string
	switch ev.Button {
	case desktop.MouseButtonPrimary:
		btnStr = "left"
	case desktop.MouseButtonSecondary:
		btnStr = "right"
	case desktop.MouseButtonTertiary:
		btnStr = "middle"
	default:
		btnStr = "unknown"
	}
	// log.Printf("Mouse Up: %s at %v", btnStr, ev.Position) // Debug

	mo.sendMouseEvent("up", btnStr, ev.Position)

	mo.mu.Lock()
	// Clear the button state only if the released button matches the currently tracked pressed button.
	// This handles cases where multiple buttons might be involved, though Fyne typically sends one event per action.
	if mo.mouseBtnState == btnStr {
		mo.mouseBtnState = ""
	}
	mo.mu.Unlock()
}

// --- Removed Tapped and TappedSecondary ---
// The functionality of Tapped (a full click) is now covered by a sequence of
// MouseDown and MouseUp events. The server will need to interpret this sequence
// if it needs to distinguish a "click" from a "drag".

// forwardVideoFeed receives video frames from the gRPC stream and writes them to the FFmpeg input.
// This function is part of the video receiving pipeline, not directly related to mouse events,
// but included as it was in the original handlers.go.
func forwardVideoFeed(stream pb.RemoteControlService_GetFeedClient, ffmpegInput io.Writer) {
	defer func() {
		log.Println("ForwardVideoFeed: Goroutine stopped.")
		if closer, ok := ffmpegInput.(io.Closer); ok {
			log.Println("ForwardVideoFeed: Closing ffmpegInput pipe writer.")
			closer.Close() // Close the pipe writer to signal EOF to FFmpeg when stream ends
		}
	}()
	log.Println("ForwardVideoFeed: Goroutine started.")

	// Buffer for reading chunks from the stream
	// The size can be tuned. Server sends chunks of video data.
	// buffer := make([]byte, 1024*64) // 64KB buffer, not directly used if frame.Data is used

	for {
		// Check stream context before Recv to allow faster exit if context is cancelled
		if stream.Context().Err() != nil {
			log.Printf("ForwardVideoFeed: Stream context cancelled before Recv. Error: %v", stream.Context().Err())
			return
		}

		frame, err := stream.Recv() // stream is pb.RemoteControlService_GetFeedClient
		if err != nil {
			if err == io.EOF {
				log.Println("ForwardVideoFeed: Video stream EOF received from server (stream.Recv() returned io.EOF).")
			} else {
				// Check for context cancellation error from gRPC
				s, ok := status.FromError(err)
				if ok && (s.Code() == codes.Canceled) {
					log.Printf("ForwardVideoFeed: Stream cancelled (gRPC status Canceled): %v", err)
				} else {
					log.Printf("ForwardVideoFeed: Error receiving video frame from server: %v", err)
				}
			}
			return // Stream finished or error
		}

		// Assuming frame.GetChunk() returns the video data bytes
		videoChunk := frame.GetData() // Use GetChunk() based on your protobuf definition for FeedResponse
		if videoChunk == nil {
			// log.Println("ForwardVideoFeed: Received nil video chunk from server.") // Can be noisy
			continue
		}
		if len(videoChunk) == 0 {
			// log.Println("ForwardVideoFeed: Received empty video chunk from server.") // Can be noisy
			continue
		}

		// log.Printf("ForwardVideoFeed: Received video chunk from server, size: %d bytes", len(videoChunk)) // Verbose
		_, writeErr := ffmpegInput.Write(videoChunk)
		if writeErr != nil {
			log.Printf("ForwardVideoFeed: Error writing video chunk to FFmpeg input pipe: %v", writeErr)
			// This typically means FFmpeg process has exited or its input pipe is broken.
			return // Stop if we can't write
		}
	}
}

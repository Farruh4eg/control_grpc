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

// mouseOverlay is a transparent widget that captures mouse events.
// It implements Focusable so it can receive focus, which helps ensure
// the window (and its canvas) correctly processes keyboard events when
// the mouse is over this overlay.
type mouseOverlay struct {
	widget.BaseWidget
	inputEventsChan chan<- *pb.FeedRequest // Channel to send mouse events
	mouseBtnState   string                 // Tracks current mouse button state
	mu              sync.Mutex             // For protecting mouseBtnState
	window          fyne.Window            // Reference to the main window to request focus on its canvas
}

// newMouseOverlay creates a new instance of mouseOverlay.
func newMouseOverlay(inputChan chan<- *pb.FeedRequest, win fyne.Window) *mouseOverlay {
	mo := &mouseOverlay{
		inputEventsChan: inputChan,
		window:          win,
	}
	mo.ExtendBaseWidget(mo)
	return mo
}

// CreateRenderer returns an empty renderer as the widget is transparent.
func (mo *mouseOverlay) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewWithoutLayout())
}

// --- fyne.Focusable interface implementation ---

// Focusable returns true, indicating this widget can receive focus.
func (mo *mouseOverlay) Focusable() bool {
	return true
}

// FocusGained is called when the widget gains focus.
func (mo *mouseOverlay) FocusGained() {
	// log.Println("MouseOverlay: Focus Gained") // Debug
	// You could add visual feedback if needed, but for a transparent overlay, it's usually not necessary.
}

// FocusLost is called when the widget loses focus.
func (mo *mouseOverlay) FocusLost() {
	// log.Println("MouseOverlay: Focus Lost") // Debug
}

// TypedKey is called when a key is typed while the widget has focus.
// For this application, global keyboard events are handled by the window's canvas,
// so this can be a no-op or log if specific overlay-focused key events are needed.
func (mo *mouseOverlay) TypedKey(ev *fyne.KeyEvent) {
	// log.Printf("MouseOverlay: TypedKey: %s", ev.Name) // Debug
	// Forward to global handler if necessary, or handle specific overlay keys.
	// For now, relying on global window handlers.
}

// TypedRune is called when a character is typed while the widget has focus.
// Similar to TypedKey, global window handlers are primary for this app.
func (mo *mouseOverlay) TypedRune(r rune) {
	// log.Printf("MouseOverlay: TypedRune: %c", r) // Debug
}

// TypedShortcut is called when a shortcut is typed while the widget has focus.
func (mo *mouseOverlay) TypedShortcut(sc fyne.Shortcut) {
	// log.Printf("MouseOverlay: TypedShortcut: %s", sc.ShortcutName()) // Debug
	// Handle shortcuts specific to this overlay if any.
}

// requestFocus attempts to set focus to this overlay widget.
func (mo *mouseOverlay) requestFocus() {
	if mo.window != nil && mo.window.Canvas() != nil {
		// log.Println("MouseOverlay: Requesting focus for itself.") // Debug
		mo.window.Canvas().Focus(mo)
	}
}

// Tapped is called for a primary button tap.
// We use it to ensure the overlay (and thus the window canvas for keyboard events) gets focus.
func (mo *mouseOverlay) Tapped(_ *fyne.PointEvent) {
	// log.Println("Overlay Tapped - Requesting focus") // Debug
	mo.requestFocus()
	// Actual click logic (mouse down/up) is handled by MouseDown/MouseUp.
}

// scaleCoordinates scales the mouse position from Fyne's coordinate system
// to the target resolution (e.g., 1920x1080).
func (mo *mouseOverlay) scaleCoordinates(pos fyne.Position) (float32, float32) {
	sz := mo.Size()
	if sz.Width == 0 || sz.Height == 0 {
		return 0, 0
	}
	targetWidth := float32(1920.0)
	targetHeight := float32(1080.0)
	scaleX := targetWidth / sz.Width
	scaleY := targetHeight / sz.Height
	return pos.X * scaleX, pos.Y * scaleY
}

// sendMouseEvent formats and sends a mouse event message.
func (mo *mouseOverlay) sendMouseEvent(eventType, btn string, pos fyne.Position) {
	sx, sy := mo.scaleCoordinates(pos)
	req := &pb.FeedRequest{
		Message:        "mouse_event", // Specific message type for mouse
		MouseX:         int32(sx),
		MouseY:         int32(sy),
		MouseBtn:       btn,
		MouseEventType: eventType,
		ClientWidth:    1920, // Or actual dynamic client width
		ClientHeight:   1080, // Or actual dynamic client height
		Timestamp:      time.Now().UnixNano(),
	}

	select {
	case mo.inputEventsChan <- req:
		// log.Printf("Mouse event queued: Type: %s, Btn: %s, Pos: (%.0f, %.0f)", eventType, btn, sx, sy) // Verbose
	default:
		log.Println("Mouse event dropped (inputEventsChan channel full)")
	}
}

// --- desktop.Hoverable interface implementation ---

func (mo *mouseOverlay) MouseIn(_ *desktop.MouseEvent) {
	// log.Println("Mouse In - Requesting focus") // Debug
	mo.requestFocus() // Request focus when mouse enters
	mo.mu.Lock()
	currentBtn := mo.mouseBtnState
	mo.mu.Unlock()
	mo.sendMouseEvent("in", currentBtn, fyne.Position{})
}

func (mo *mouseOverlay) MouseMoved(ev *desktop.MouseEvent) {
	// log.Printf("Mouse Moved to %v", ev.Position) // Debug
	mo.mu.Lock()
	currentBtn := mo.mouseBtnState
	mo.mu.Unlock()
	mo.sendMouseEvent("move", currentBtn, ev.Position)
}

func (mo *mouseOverlay) MouseOut() {
	// log.Println("Mouse Out") // Debug
	mo.mu.Lock()
	currentBtn := mo.mouseBtnState
	mo.mu.Unlock()
	mo.sendMouseEvent("out", currentBtn, fyne.Position{})
}

// --- desktop.Mouseable interface implementation ---

func (mo *mouseOverlay) MouseDown(ev *desktop.MouseEvent) {
	// log.Println("Mouse Down - Requesting focus") // Debug
	mo.requestFocus() // Ensure focus on mouse down
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
	mo.mu.Lock()
	mo.mouseBtnState = btnStr
	mo.mu.Unlock()
	mo.sendMouseEvent("down", btnStr, ev.Position)
}

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
	mo.sendMouseEvent("up", btnStr, ev.Position)
	mo.mu.Lock()
	if mo.mouseBtnState == btnStr {
		mo.mouseBtnState = ""
	}
	mo.mu.Unlock()
}

// forwardVideoFeed remains unchanged.
func forwardVideoFeed(stream pb.RemoteControlService_GetFeedClient, ffmpegInput io.Writer) {
	defer func() {
		log.Println("ForwardVideoFeed: Goroutine stopped.")
		if closer, ok := ffmpegInput.(io.Closer); ok {
			log.Println("ForwardVideoFeed: Closing ffmpegInput pipe writer.")
			closer.Close()
		}
	}()
	log.Println("ForwardVideoFeed: Goroutine started.")

	for {
		if stream.Context().Err() != nil {
			log.Printf("ForwardVideoFeed: Stream context cancelled before Recv. Error: %v", stream.Context().Err())
			return
		}

		frame, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				log.Println("ForwardVideoFeed: Video stream EOF received from server.")
			} else {
				s, ok := status.FromError(err)
				if ok && (s.Code() == codes.Canceled) {
					log.Printf("ForwardVideoFeed: Stream cancelled (gRPC status Canceled): %v", err)
				} else {
					log.Printf("ForwardVideoFeed: Error receiving video frame from server: %v", err)
				}
			}
			return
		}

		videoChunk := frame.GetData()
		if videoChunk == nil || len(videoChunk) == 0 {
			continue
		}

		_, writeErr := ffmpegInput.Write(videoChunk)
		if writeErr != nil {
			log.Printf("ForwardVideoFeed: Error writing video chunk to FFmpeg input pipe: %v", writeErr)
			return
		}
	}
}

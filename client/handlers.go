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
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

type mouseOverlay struct {
	widget.BaseWidget
	inputEventsChan chan<- *pb.FeedRequest
	mouseBtnState   string
	mu              sync.Mutex
	window          fyne.Window
}

func newMouseOverlay(inputChan chan<- *pb.FeedRequest, win fyne.Window) *mouseOverlay {
	mo := &mouseOverlay{
		inputEventsChan: inputChan,
		window:          win,
	}
	mo.ExtendBaseWidget(mo)
	return mo
}

func (mo *mouseOverlay) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewWithoutLayout())
}

func (mo *mouseOverlay) Focusable() bool {
	return true
}

func (mo *mouseOverlay) FocusGained() {

}

func (mo *mouseOverlay) FocusLost() {

}

func (mo *mouseOverlay) TypedKey(ev *fyne.KeyEvent) {

}

func (mo *mouseOverlay) TypedRune(r rune) {

}

func (mo *mouseOverlay) TypedShortcut(sc fyne.Shortcut) {

}

func (mo *mouseOverlay) requestFocus() {
	if mo.window != nil && mo.window.Canvas() != nil {

		mo.window.Canvas().Focus(mo)
	}
}

func (mo *mouseOverlay) Tapped(_ *fyne.PointEvent) {

	mo.requestFocus()

}

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

func (mo *mouseOverlay) sendMouseEvent(eventType, btn string, pos fyne.Position) {
	sx, sy := mo.scaleCoordinates(pos)
	req := &pb.FeedRequest{
		Message:        "mouse_event",
		MouseX:         int32(sx),
		MouseY:         int32(sy),
		MouseBtn:       btn,
		MouseEventType: eventType,
		ClientWidth:    1920,
		ClientHeight:   1080,
		Timestamp:      time.Now().UnixNano(),
	}

	select {
	case mo.inputEventsChan <- req:

	default:
		log.Println("Mouse event dropped (inputEventsChan channel full)")
	}
}

func (mo *mouseOverlay) MouseIn(_ *desktop.MouseEvent) {

	mo.requestFocus()
	mo.mu.Lock()
	currentBtn := mo.mouseBtnState
	mo.mu.Unlock()
	mo.sendMouseEvent("in", currentBtn, fyne.Position{})
}

func (mo *mouseOverlay) MouseMoved(ev *desktop.MouseEvent) {

	mo.mu.Lock()
	currentBtn := mo.mouseBtnState
	mo.mu.Unlock()
	mo.sendMouseEvent("move", currentBtn, ev.Position)
}

func (mo *mouseOverlay) MouseOut() {

	mo.mu.Lock()
	currentBtn := mo.mouseBtnState
	mo.mu.Unlock()
	mo.sendMouseEvent("out", currentBtn, fyne.Position{})
}

func (mo *mouseOverlay) MouseDown(ev *desktop.MouseEvent) {

	mo.requestFocus()
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

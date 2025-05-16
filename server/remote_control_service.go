package main

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"log"
	"time"

	"github.com/go-vgo/robotgo" // For controlling mouse/keyboard and getting screen size

	pb "control_grpc/gen/proto"  // Assuming this path is correct
	"control_grpc/server/screen" // Assuming this is your local package for screen capture
)

// GetFeed handles the bidirectional stream for remote control.
// It receives mouse/keyboard events from the client and sends screen frames to the client.
func (s *server) GetFeed(stream pb.RemoteControlService_GetFeedServer) error {
	serverWidth, serverHeight := robotgo.GetScreenSize()
	log.Printf("Server screen dimensions: %dx%d", serverWidth, serverHeight)

	capture, err := screen.NewScreenCapture()
	if err != nil {
		log.Printf("Error initializing screen capture: %v", err)
		return status.Errorf(codes.Internal, "Failed to initialize screen capture: %v", err)
	}
	defer capture.Close()

	// Receive initial message from client (e.g., client dimensions)
	reqMsgInit, err := stream.Recv()
	if err != nil {
		if err == io.EOF {
			log.Println("Client closed stream before init.")
			return nil
		}
		log.Printf("Failed to receive initial message: %v", err)
		return status.Errorf(codes.InvalidArgument, "Failed to receive initial message: %v", err)
	}
	log.Printf("Received init message from client: Width=%d, Height=%d", reqMsgInit.GetClientWidth(), reqMsgInit.GetClientHeight())

	scaleX, scaleY := getScaleFactors(serverWidth, serverHeight, reqMsgInit)
	log.Printf("Calculated scale factors: ScaleX=%.2f, ScaleY=%.2f", scaleX, scaleY)

	// Channel for mouse events received from the client
	mouseEvents := make(chan *pb.FeedRequest, 120) // Buffered channel

	// Goroutine to process mouse events from the channel
	go handleMouseEvents(mouseEvents, scaleX, scaleY)

	// Goroutine to receive mouse events from the stream and put them into the channel
	go receiveMouseEvents(stream, mouseEvents)

	// Send screen feed in the current goroutine
	return sendScreenFeed(stream, capture)
}

// getScaleFactors calculates the scaling factors based on server and client screen dimensions.
func getScaleFactors(serverWidth, serverHeight int, reqMsgInit *pb.FeedRequest) (float32, float32) {
	if reqMsgInit.GetClientWidth() == 0 || reqMsgInit.GetClientHeight() == 0 {
		log.Println("Client width or height is zero, using 1.0 for scale factors.")
		return 1.0, 1.0 // Avoid division by zero, default to no scaling
	}
	scaleX := float32(serverWidth) / float32(reqMsgInit.GetClientWidth())
	scaleY := float32(serverHeight) / float32(reqMsgInit.GetClientHeight())
	return scaleX, scaleY
}

// handleMouseEvents processes mouse events received from the client.
func handleMouseEvents(mouseEvents chan *pb.FeedRequest, scaleX, scaleY float32) {
	log.Println("Mouse event handler goroutine started.")
	defer log.Println("Mouse event handler goroutine stopped.")
	for reqMsg := range mouseEvents {
		// Scale client coordinates to server coordinates
		serverX := int(float32(reqMsg.GetMouseX()) * scaleX)
		serverY := int(float32(reqMsg.GetMouseY()) * scaleY)

		// log.Printf("Client Event: Type=%s, Btn=%s, ClientX=%d, ClientY=%d -> ServerX=%d, ServerY=%d",
		// 	reqMsg.GetMouseEventType(), reqMsg.GetMouseBtn(), reqMsg.GetMouseX(), reqMsg.GetMouseY(), serverX, serverY) // Verbose

		robotgo.Move(serverX, serverY)

		eventType := reqMsg.GetMouseEventType()
		mouseBtn := reqMsg.GetMouseBtn() // "left", "right", "middle"

		if eventType == "down" || eventType == "press" { // "press" for compatibility with some clients
			// log.Printf("Mouse Down: %s at %d,%d", mouseBtn, serverX, serverY) // Verbose
			robotgo.MouseDown(mouseBtn)
		} else if eventType == "up" || eventType == "release" { // "release" for compatibility
			// log.Printf("Mouse Up: %s at %d,%d", mouseBtn, serverX, serverY) // Verbose
			robotgo.MouseUp(mouseBtn)
		}
		// "click" can be a sequence of down and up.
		// "drag" is typically a move event while a button is held down. robotgo.Move handles this.
	}
}

// receiveMouseEvents continuously receives mouse event messages from the gRPC stream.
func receiveMouseEvents(stream pb.RemoteControlService_GetFeedServer, mouseEvents chan *pb.FeedRequest) {
	log.Println("Mouse event receiver goroutine started.")
	defer log.Println("Mouse event receiver goroutine stopped.")
	defer close(mouseEvents) // Close the channel when this goroutine exits

	for {
		reqMsg, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				log.Println("Client closed the stream (EOF in receiveMouseEvents).")
				return // Client closed the stream
			}
			// Check for context cancellation (client disconnected)
			s, ok := status.FromError(err)
			if ok && (s.Code() == codes.Canceled || s.Code() == codes.Unavailable) {
				log.Printf("Stream canceled or unavailable in receiveMouseEvents: %v", err)
				return
			}
			log.Printf("Error receiving mouse event from stream: %v", err)
			return // Other critical error
		}

		// Send received message to the mouseEvents channel for processing
		select {
		case mouseEvents <- reqMsg:
			// Event successfully queued
		default:
			log.Println("Mouse event channel full, dropping event. This might indicate slow processing.")
		}
	}
}

// sendScreenFeed captures and sends screen frames to the client.
func sendScreenFeed(stream pb.RemoteControlService_GetFeedServer, capture *screen.ScreenCapture) error {
	log.Println("Screen feed sender goroutine started.")
	defer log.Println("Screen feed sender goroutine stopped.")

	// Buffer for frame data. Size might need adjustment based on typical frame sizes.
	frameBuffer := make([]byte, 2*1024*1024)   // 2MB buffer, adjust as needed
	ticker := time.NewTicker(time.Second / 30) // Target ~30 FPS
	defer ticker.Stop()

	var frameCounter int32 = 0
	for {
		select {
		case <-ticker.C:
			n, err := capture.ReadFrame(frameBuffer)
			if err != nil {
				if err == io.EOF { // Should not happen with continuous capture
					log.Println("Screen capture source reported EOF.")
					return status.Errorf(codes.Internal, "Screen capture source EOF")
				}
				log.Printf("Error reading frame from screen capture: %v", err)
				// Decide if this error is fatal for the stream
				// For now, we continue, but some errors might require stopping.
				continue
			}

			if n == 0 {
				// log.Println("Read 0 bytes from screen capture, skipping frame.") // Can be spammy
				continue
			}

			// log.Printf("Sending frame %d, size %d bytes, HwAccel: %s", frameCounter, n, screen.Accel) // Verbose

			err = stream.Send(&pb.FeedResponse{
				Data:        frameBuffer[:n],
				FrameNumber: frameCounter,
				Timestamp:   time.Now().UnixNano(),
				ContentType: "video/mp2t", // MPEG Transport Stream
				HwAccel:     screen.Accel, // Hardware acceleration info from screen package
			})
			if err != nil {
				// Check for client disconnection or stream errors
				s, ok := status.FromError(err)
				if ok && (s.Code() == codes.Canceled || s.Code() == codes.Unavailable) {
					log.Printf("Client disconnected or stream unavailable: %v", err)
					return nil // Graceful exit if client disconnected
				}
				log.Printf("Error sending frame to client: %v", err)
				return status.Errorf(codes.Internal, "Failed to send frame: %v", err) // Propagate other errors
			}
			frameCounter++
		case <-stream.Context().Done():
			log.Printf("Stream context done (client likely disconnected): %v", stream.Context().Err())
			return nil // Graceful exit
		}
	}
}

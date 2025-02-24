package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/go-vgo/robotgo"
	hook "github.com/robotn/gohook"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "control_grpc/gen/proto"
)

type MouseTracker struct {
	mu       sync.Mutex
	mouseBtn string
}

func main() {
	tlsCredentials, err := loadTLSCredentials()
	if err != nil {
		log.Fatal("Cannot load TLS credentials: ", err)
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(tlsCredentials)}

	serverAddr := "localhost:32212"
	conn, err := grpc.NewClient(serverAddr, opts...)
	if err != nil {
		log.Fatal("Could not connect to server")
	}
	defer conn.Close()

	client := pb.NewRemoteControlServiceClient(conn)
	stream, err := client.GetFeed(context.Background())
	if err != nil {
		log.Fatalf("Error creating stream: %v", err)
	}

	cmd, pipe, err := createFFPlayCommand()
	if err != nil {
		log.Fatal("Error setting up FFplay command: ", err)
	}
	defer cmd.Wait()

	waitc := make(chan struct{})
	go forwardVideoFeed(stream, pipe, waitc)

	go trackAndPollMouse(stream)

	<-waitc
	stream.CloseSend()
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

func loadTLSCredentials() (credentials.TransportCredentials, error) {
	clientCert, err := tls.LoadX509KeyPair("client.crt", "client.key")
	if err != nil {
		return nil, err
	}

	serverCACert, err := os.ReadFile("server.crt")
	if err != nil {
		return nil, err
	}

	serverCertPool := x509.NewCertPool()
	if !serverCertPool.AppendCertsFromPEM(serverCACert) {
		return nil, err
	}

	config := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      serverCertPool,
	}

	return credentials.NewTLS(config), nil
}

func createFFPlayCommand() (*exec.Cmd, io.WriteCloser, error) {
	cmd := exec.Command("../bin/ffplay.exe",
		"-rtbufsize", "128k",
		"-an",
		"-f", "h264",
		"-probesize", "32",
		"-sync", "ext",
		"-fflags", "nobuffer",
		"-i", "pipe:0")
	pipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, pipe, nil
}

// forwardVideoFeed continuously receives video frames from the server
// and writes them to FFplay's pipe.
func forwardVideoFeed(stream pb.RemoteControlService_GetFeedClient, pipe io.WriteCloser, waitc chan struct{}) {
	for {
		receiveTime := time.Now()
		frame, err := stream.Recv()
		if err == io.EOF {
			close(waitc)
			return
		}
		if err != nil {
			log.Fatalf("Failed to receive feed: %v", err)
		}
		if _, err := pipe.Write(frame.Data); err != nil {
			log.Fatalf("Failed to send frame to FFmpeg: %v", err)
		}
		log.Printf("Time since last frame: %v, frame: %d", time.Since(receiveTime), frame.GetFrameNumber())
	}
}

// sendMouseEvent sends a mouse event message over the gRPC stream.
// It obtains the current mouse location at the time of sending.
func sendMouseEvent(stream pb.RemoteControlService_GetFeedClient, eventType, btn string, w, h int) error {
	mouseX, mouseY := robotgo.Location()
	return stream.Send(&pb.FeedRequest{
		Message:        "mouse_event",
		Success:        true,
		MouseX:         int32(mouseX),
		MouseY:         int32(mouseY),
		MouseBtn:       btn,
		MouseEventType: eventType,
		ClientWidth:    int32(w),
		ClientHeight:   int32(h),
		Timestamp:      time.Now().UnixNano(),
	})
}

// trackAndPollMouse listens for mouse events (using gohook) and
// periodically polls the current mouse location, sending updates to the server.
func trackAndPollMouse(stream pb.RemoteControlService_GetFeedClient) {
	// Start hook to capture mouse events.
	evChan := hook.Start()
	defer hook.End()

	w, h := robotgo.GetScreenSize()

	// Ticker for periodic polling.
	ticker := time.NewTicker(time.Second / 30)
	defer ticker.Stop()

	mt := &MouseTracker{}

	for {
		select {
		// Handle mouse events.
		case ev, ok := <-evChan:
			if !ok {
				return
			}
			var eventType string
			if ev.Kind == hook.MouseDown {
				var btn string
				switch ev.Button {
				case hook.MouseMap["left"]:
					btn = "left"
				case hook.MouseMap["right"]:
					btn = "right"
				case hook.MouseMap["middle"]:
					btn = "middle"
				}
				eventType = "press"
				mt.setButton(btn)
			} else if ev.Kind == hook.MouseUp {
				eventType = "release"
				mt.setButton("")
			}
			if err := sendMouseEvent(stream, eventType, mt.getButton(), w, h); err != nil {
				log.Fatalf("Failed to send mouse event: %v", err)
			}
		// Periodically poll the mouse location.
		case <-ticker.C:
			if err := sendMouseEvent(stream, "mouse_event", mt.getButton(), w, h); err != nil {
				log.Fatalf("Failed to send polled mouse data: %v", err)
			}
		}
	}
}

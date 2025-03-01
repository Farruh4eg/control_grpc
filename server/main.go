package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"github.com/go-vgo/robotgo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"

	pb "control_grpc/gen/proto"
	"control_grpc/server/screen"
)

type server struct {
	pb.UnimplementedAuthServiceServer
	pb.UnimplementedRemoteControlServiceServer
}

var port = flag.Int("port", 32212, "The server port")

func main() {
	flag.Parse()

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	tlsCredentials, err := loadTLSCredentials()
	if err != nil {
		log.Fatal("Cannot load TLS credentials: ", err)
	}

	grpcServer := grpc.NewServer(grpc.Creds(tlsCredentials))
	pb.RegisterAuthServiceServer(grpcServer, &server{})
	pb.RegisterRemoteControlServiceServer(grpcServer, &server{})
	reflection.Register(grpcServer)

	fmt.Printf("Server listening at %v\n", lis.Addr())
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}

func (s *server) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	username, password := req.GetUsername(), req.GetPassword()
	fmt.Printf("Received login request for user: %s\n", username)

	if isValidUser(username, password) {
		return &pb.LoginResponse{Success: true}, nil
	}
	return &pb.LoginResponse{Success: false, ErrorMessage: "Invalid credentials"}, nil
}

func isValidUser(username, password string) bool {
	return username == "test" && password == "password"
}

func loadTLSCredentials() (credentials.TransportCredentials, error) {
	serverCert, err := tls.LoadX509KeyPair("server.crt", "server.key")
	if err != nil {
		return nil, err
	}

	clientCACert, err := os.ReadFile("client.crt")
	if err != nil {
		return nil, err
	}

	clientCertPool := x509.NewCertPool()
	clientCertPool.AppendCertsFromPEM(clientCACert)

	config := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCertPool,
	}

	return credentials.NewTLS(config), nil
}

func (s *server) GetFeed(stream pb.RemoteControlService_GetFeedServer) error {
	serverWidth, serverHeight := robotgo.GetScreenSize()
	capture, err := screen.NewScreenCapture()
	if err != nil {
		log.Printf("Error initializing screen capture: %v", err)
		return err
	}
	defer capture.Close()

	reqMsgInit, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("Failed to receive initial message: %v", err)
	}
	scaleX, scaleY := getScaleFactors(serverWidth, serverHeight, reqMsgInit)
	mouseEvents := make(chan *pb.FeedRequest, 100)

	go handleMouseEvents(mouseEvents, scaleX, scaleY)
	go receiveMouseEvents(stream, mouseEvents)

	return sendScreenFeed(stream, capture)
}

func getScaleFactors(serverWidth, serverHeight int, reqMsgInit *pb.FeedRequest) (float32, float32) {
	return float32(serverWidth) / float32(reqMsgInit.GetClientWidth()), float32(serverHeight) / float32(reqMsgInit.GetClientHeight())
}

func handleMouseEvents(mouseEvents chan *pb.FeedRequest, scaleX, scaleY float32) {
	for reqMsg := range mouseEvents {
		serverX, serverY := int(float32(reqMsg.GetMouseX())*scaleX), int(float32(reqMsg.GetMouseY())*scaleY)
		log.Printf("Moving cursor to: X=%d, Y=%d", serverX, serverY)
		robotgo.Move(serverX, serverY)
		if reqMsg.GetMouseEventType() == "press" {
			robotgo.MouseDown(reqMsg.GetMouseBtn())
		} else if reqMsg.GetMouseEventType() == "release" {
			robotgo.MouseUp(reqMsg.GetMouseBtn())
		}
	}
}

func receiveMouseEvents(stream pb.RemoteControlService_GetFeedServer, mouseEvents chan *pb.FeedRequest) {
	for {
		reqMsg, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return
			}
			log.Printf("Error receiving message: %v", err)
			return
		}
		select {
		case mouseEvents <- reqMsg:

		default:
			log.Println("Mouse event channel full, dropping event")
		}
	}
}

func sendScreenFeed(stream pb.RemoteControlService_GetFeedServer, capture *screen.ScreenCapture) error {
	frameBuffer := make([]byte, 64*1024)
	ticker := time.NewTicker(time.Second / 60)
	defer ticker.Stop()

	frameCounter := 0
	for range ticker.C {
		n, err := capture.ReadFrame(frameBuffer)
		if err != nil {
			log.Printf("Error reading frame: %v", err)
			return err
		}

		err = stream.Send(&pb.FeedResponse{
			Data:        frameBuffer[:n],
			FrameNumber: int32(frameCounter),
			Timestamp:   time.Now().UnixNano(),
			ContentType: "video/mp2t",
			HwAccel:     screen.Accel,
		})
		if err != nil {
			log.Printf("Error sending frame: %v", err)
			return err
		}
		frameCounter++
	}
	return nil
}

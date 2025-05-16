package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	pb "control_grpc/gen/proto" // Assuming this path is correct

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
)

//go:embed server.crt
var serverCertEmbed []byte

//go:embed server.key
var serverKeyEmbed []byte

//go:embed client.crt
var clientCACertEmbed []byte

type server struct {
	pb.UnimplementedAuthServiceServer
	pb.UnimplementedRemoteControlServiceServer
	pb.UnimplementedFileTransferServiceServer
	localGrpcAddr string
}

var (
	portFlag          = flag.Int("port", 32212, "The server port for direct gRPC connections")
	enableRelay       = flag.Bool("relay", false, "Enable relay mode to connect through a relay server")
	relayServerAddr   = flag.String("relayServer", "localhost:34000", "Address of the relay server's control port (IP:PORT)")
	hostID            = flag.String("hostID", "defaultHost", "Unique ID for this host when using relay mode")
	fyneApp           fyne.App
	serverStatusLabel *widget.Label
	relayStatusLabel  *widget.Label
)

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	localGrpcListener, err := net.Listen("tcp", fmt.Sprintf(":%d", *portFlag))
	if err != nil {
		log.Fatalf("FATAL: Failed to listen on port %d: %v", *portFlag, err)
	}
	localGrpcServerAddr := localGrpcListener.Addr().String()
	log.Printf("INFO: Local gRPC server will listen on %s", localGrpcServerAddr)

	tlsCredentials, err := loadTLSCredentialsFromEmbed()
	if err != nil {
		log.Fatalf("FATAL: Cannot load TLS credentials: %v", err)
	}

	opts := []grpc.ServerOption{
		grpc.Creds(tlsCredentials),
		grpc.MaxSendMsgSize(1024 * 1024 * 10),
		grpc.MaxRecvMsgSize(1024 * 1024 * 10),
	}

	grpcServer := grpc.NewServer(opts...)
	s := &server{localGrpcAddr: localGrpcServerAddr}

	pb.RegisterAuthServiceServer(grpcServer, s)
	pb.RegisterRemoteControlServiceServer(grpcServer, s)
	pb.RegisterFileTransferServiceServer(grpcServer, s)
	reflection.Register(grpcServer)

	fyneApp = app.New()
	fyneWindow := fyneApp.NewWindow("gRPC Server Status")

	serverStatusLabel = widget.NewLabel(fmt.Sprintf("Direct gRPC: Listening on %s", localGrpcServerAddr))
	serverStatusLabel.Alignment = fyne.TextAlignCenter
	relayStatusLabel = widget.NewLabel("Relay: Disabled")
	if *enableRelay {
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Connecting to %s as '%s'...", *relayServerAddr, *hostID))
	}
	relayStatusLabel.Alignment = fyne.TextAlignCenter

	quitButton := widget.NewButton("Shutdown Server", func() {
		log.Println("INFO: Shutdown button clicked. Stopping server...")
		grpcServer.GracefulStop()
		fyneApp.Quit()
	})

	fyneWindow.SetContent(container.NewVBox(
		serverStatusLabel,
		relayStatusLabel,
		quitButton,
	))
	fyneWindow.Resize(fyne.NewSize(450, 200))
	fyneWindow.SetFixedSize(true)
	fyneWindow.CenterOnScreen()

	fyneWindow.SetOnClosed(func() {
		log.Println("INFO: Fyne window closed by user. Initiating server shutdown...")
		go grpcServer.GracefulStop()
		log.Println("INFO: Server shutdown process initiated from OnClosed.")
	})

	go func() {
		log.Printf("INFO: gRPC Server starting for direct connections at %s", localGrpcServerAddr)
		if err := grpcServer.Serve(localGrpcListener); err != nil {
			log.Printf("ERROR: Failed to serve direct gRPC: %v", err)
			fyneApp.SendNotification(&fyne.Notification{
				Title:   "gRPC Server Error",
				Content: fmt.Sprintf("Failed to serve direct gRPC: %v", err),
			})
			serverStatusLabel.SetText(fmt.Sprintf("Direct gRPC: Error - %v", err))
		}
		log.Println("INFO: Direct gRPC Server has stopped.")
	}()

	if *enableRelay {
		if *hostID == "" {
			*hostID = "defaultHost"
		}
		go s.manageRelayRegistrationAndTunnels(*relayServerAddr, *hostID, localGrpcServerAddr)
	}

	log.Println("INFO: Starting Fyne application UI...")
	fyneWindow.ShowAndRun()

	log.Println("INFO: Fyne application has exited.")
	log.Println("INFO: Ensuring gRPC server is stopped...")
	grpcServer.GracefulStop()
	log.Println("INFO: gRPC server shutdown complete. Exiting application.")
}

func (s *server) manageRelayRegistrationAndTunnels(relayCtrlAddrFull, hostID, localGrpcSvcAddr string) {
	var controlConn net.Conn
	var err error

	for {
		log.Printf("INFO: [Relay] Attempting to connect to relay control server %s...", relayCtrlAddrFull)
		if relayStatusLabel != nil {
			relayStatusLabel.SetText(fmt.Sprintf("Relay: Connecting to %s as '%s'...", relayCtrlAddrFull, hostID))
		}

		controlConn, err = net.DialTimeout("tcp", relayCtrlAddrFull, 10*time.Second)
		if err != nil {
			log.Printf("WARN: [Relay] Failed to connect to relay control server %s: %v. Retrying in 10s...", relayCtrlAddrFull, err)
			if relayStatusLabel != nil {
				relayStatusLabel.SetText(fmt.Sprintf("Relay: Failed to connect. Retrying..."))
			}
			time.Sleep(10 * time.Second)
			continue
		}
		break
	}
	defer controlConn.Close()
	log.Printf("INFO: [Relay] Connected to relay control server: %s", controlConn.RemoteAddr())
	if relayStatusLabel != nil {
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Connected to %s. Registering as '%s'...", relayCtrlAddrFull, hostID))
	}

	registerCmd := fmt.Sprintf("REGISTER_HOST %s\n", hostID)
	_, err = fmt.Fprint(controlConn, registerCmd)
	if err != nil {
		log.Printf("ERROR: [Relay] Failed to send REGISTER_HOST command: %v", err)
		if relayStatusLabel != nil {
			relayStatusLabel.SetText(fmt.Sprintf("Relay: Registration failed."))
		}
		return
	}
	log.Printf("INFO: [Relay] Sent: %s", strings.TrimSpace(registerCmd))

	reader := bufio.NewReader(controlConn)
	for {
		response, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				log.Printf("INFO: [Relay] Control connection to relay server closed (EOF). Reconnecting...")
			} else {
				log.Printf("ERROR: [Relay] Error reading from relay control connection: %v. Reconnecting...", err)
			}
			controlConn.Close()
			go s.manageRelayRegistrationAndTunnels(relayCtrlAddrFull, hostID, localGrpcSvcAddr)
			return
		}

		response = strings.TrimSpace(response)
		log.Printf("INFO: [Relay] Received from relay: %s", response)
		parts := strings.Fields(response)
		if len(parts) == 0 {
			continue
		}
		command := parts[0]

		switch command {
		case "HOST_REGISTERED":
			log.Printf("INFO: [Relay] Successfully registered with relay server as Host ID: %s", parts[1])
			if relayStatusLabel != nil {
				relayStatusLabel.SetText(fmt.Sprintf("Relay: Registered as '%s'. Waiting for clients.", hostID))
			}
		case "CREATE_TUNNEL": // CREATE_TUNNEL <dynamic_port_str> <session_token>
			if len(parts) < 3 {
				log.Printf("ERROR: [Relay] Invalid CREATE_TUNNEL command: %s", response)
				continue
			}
			dynamicPortStr := parts[1]
			sessionToken := parts[2]
			log.Printf("INFO: [Relay] Received CREATE_TUNNEL for session %s, relay dynamic port %s", sessionToken, dynamicPortStr)

			// Host needs to connect to the relay server's public IP on this dynamic port.
			// The relay server's public IP is the host part of relayCtrlAddrFull (e.g., from -relayServer flag).
			relayHostIP, _, err := net.SplitHostPort(relayCtrlAddrFull)
			if err != nil {
				log.Printf("ERROR: [Relay] Could not parse host IP from relayCtrlAddrFull '%s': %v", relayCtrlAddrFull, err)
				continue
			}
			relayDataAddrForHost := net.JoinHostPort(relayHostIP, dynamicPortStr)
			log.Printf("INFO: [Relay] Host will connect to relay data endpoint: %s for session %s", relayDataAddrForHost, sessionToken)

			if relayStatusLabel != nil {
				relayStatusLabel.SetText(fmt.Sprintf("Relay: Client connecting via session %s...", sessionToken[:8]))
			}
			go s.handleHostSideTunnel(localGrpcSvcAddr, relayDataAddrForHost, sessionToken)
		default:
			log.Printf("WARN: [Relay] Unknown command from relay server: %s", response)
		}
	}
}

func (s *server) handleHostSideTunnel(localGrpcServiceAddr, relayDataAddrForHost, sessionToken string) {
	log.Printf("INFO: [Tunnel %s] Host-side: Attempting to connect to relay data endpoint %s", sessionToken, relayDataAddrForHost)
	hostProxyConn, err := net.DialTimeout("tcp", relayDataAddrForHost, 10*time.Second)
	if err != nil {
		log.Printf("ERROR: [Tunnel %s] Host-side: Failed to connect to relay data endpoint %s: %v", sessionToken, relayDataAddrForHost, err)
		return
	}
	defer hostProxyConn.Close()
	log.Printf("INFO: [Tunnel %s] Host-side: Connected to relay data endpoint: %s", sessionToken, hostProxyConn.RemoteAddr())

	identCmd := fmt.Sprintf("SESSION_TOKEN %s HOST_PROXY\n", sessionToken)
	_, err = fmt.Fprint(hostProxyConn, identCmd)
	if err != nil {
		log.Printf("ERROR: [Tunnel %s] Host-side: Failed to send session token identification: %v", sessionToken, err)
		return
	}
	log.Printf("INFO: [Tunnel %s] Host-side: Sent identification: %s", sessionToken, strings.TrimSpace(identCmd))

	log.Printf("INFO: [Tunnel %s] Host-side: Connecting to local gRPC service at %s", sessionToken, localGrpcServiceAddr)
	localServiceConn, err := net.DialTimeout("tcp", localGrpcServiceAddr, 5*time.Second)
	if err != nil {
		log.Printf("ERROR: [Tunnel %s] Host-side: Failed to connect to local gRPC service %s: %v", sessionToken, localGrpcServiceAddr, err)
		return
	}
	defer localServiceConn.Close()
	log.Printf("INFO: [Tunnel %s] Host-side: Connected to local gRPC service. Starting proxy.", sessionToken)
	if relayStatusLabel != nil {
		originalText := relayStatusLabel.Text
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Active session %s", sessionToken[:8]))
		go func() {
			time.Sleep(5 * time.Second)
			if relayStatusLabel != nil && relayStatusLabel.Text == fmt.Sprintf("Relay: Active session %s", sessionToken[:8]) {
				relayStatusLabel.SetText(originalText)
			}
		}()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer hostProxyConn.Close()
		defer localServiceConn.Close()
		written, errCopy := io.Copy(localServiceConn, hostProxyConn)
		if errCopy != nil && errCopy != io.EOF && !strings.Contains(errCopy.Error(), "use of closed network connection") {
			log.Printf("ERROR: [Tunnel %s] Host-side: Error copying from relay to local: %v (bytes: %d)", sessionToken, errCopy, written)
		} else {
			log.Printf("INFO: [Tunnel %s] Host-side: Finished copying from relay to local. Bytes: %d", sessionToken, written)
		}
	}()
	go func() {
		defer wg.Done()
		defer localServiceConn.Close()
		defer hostProxyConn.Close()
		written, errCopy := io.Copy(hostProxyConn, localServiceConn)
		if errCopy != nil && errCopy != io.EOF && !strings.Contains(errCopy.Error(), "use of closed network connection") {
			log.Printf("ERROR: [Tunnel %s] Host-side: Error copying from local to relay: %v (bytes: %d)", sessionToken, errCopy, written)
		} else {
			log.Printf("INFO: [Tunnel %s] Host-side: Finished copying from local to relay. Bytes: %d", sessionToken, written)
		}
	}()
	wg.Wait()
	log.Printf("INFO: [Tunnel %s] Host-side: Proxying finished.", sessionToken)
	if relayStatusLabel != nil && relayStatusLabel.Text == fmt.Sprintf("Relay: Active session %s", sessionToken[:8]) {
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Registered as '%s'. Waiting for clients.", *hostID))
	}
}

func loadTLSCredentialsFromEmbed() (credentials.TransportCredentials, error) {
	serverCert, err := tls.X509KeyPair(serverCertEmbed, serverKeyEmbed)
	if err != nil {
		return nil, fmt.Errorf("failed to load server key pair: %w", err)
	}
	clientCertPool := x509.NewCertPool()
	if !clientCertPool.AppendCertsFromPEM(clientCACertEmbed) {
		return nil, fmt.Errorf("failed to append client CA cert: %w", err)
	}
	config := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCertPool,
		MinVersion:   tls.VersionTLS12,
		ServerName:   "localhost", // ServerName for server's own TLS config, less critical here
	}
	return credentials.NewTLS(config), nil
}

func (s *server) Ping(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{ClientTimestampNano: req.GetClientTimestampNano()}, nil
}

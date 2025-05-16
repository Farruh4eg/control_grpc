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

// server struct now embeds the unimplemented server types.
type server struct {
	pb.UnimplementedAuthServiceServer
	pb.UnimplementedRemoteControlServiceServer
	pb.UnimplementedFileTransferServiceServer
	localGrpcAddr string // To store the address of the local gRPC server
}

// Command-line flags
var (
	port              = flag.Int("port", 32212, "The server port for direct gRPC connections")
	enableRelay       = flag.Bool("relay", false, "Enable relay mode to connect through a relay server")
	relayServerAddr   = flag.String("relayServer", "localhost:34000", "Address of the relay server's control port")
	hostID            = flag.String("hostID", "defaultHost", "Unique ID for this host when using relay mode")
	fyneApp           fyne.App // Declare fyneApp globally or pass it around
	serverStatusLabel *widget.Label
	relayStatusLabel  *widget.Label
)

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// --- Initialize Listener for direct gRPC ---
	localGrpcListener, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("FATAL: Failed to listen on port %d: %v", *port, err)
	}
	localGrpcServerAddr := localGrpcListener.Addr().String()
	log.Printf("INFO: Local gRPC server will listen on %s", localGrpcServerAddr)

	// --- Load TLS Credentials ---
	tlsCredentials, err := loadTLSCredentialsFromEmbed()
	if err != nil {
		log.Fatalf("FATAL: Cannot load TLS credentials: %v", err)
	}

	// --- gRPC Server Options ---
	opts := []grpc.ServerOption{
		grpc.Creds(tlsCredentials),
		grpc.MaxSendMsgSize(1024 * 1024 * 10), // 10MB
		grpc.MaxRecvMsgSize(1024 * 1024 * 10), // 10MB
	}

	// --- Create gRPC Server ---
	grpcServer := grpc.NewServer(opts...)
	s := &server{localGrpcAddr: localGrpcServerAddr} // Store local gRPC address

	// --- Register Services ---
	pb.RegisterAuthServiceServer(grpcServer, s)
	pb.RegisterRemoteControlServiceServer(grpcServer, s)
	pb.RegisterFileTransferServiceServer(grpcServer, s)
	reflection.Register(grpcServer)

	// --- Fyne GUI Setup ---
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
		// Add logic here to close relay control connection if active
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
		// Add logic here to close relay control connection if active
		log.Println("INFO: Server shutdown process initiated from OnClosed.")
	})

	// --- Start gRPC Server for direct connections ---
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

	// --- Relay Mode Logic ---
	if *enableRelay {
		if *hostID == "" {
			log.Println("WARN: Relay mode enabled but no hostID provided. Using 'defaultHost'.")
			*hostID = "defaultHost" // Or make it a fatal error
		}
		// Run relay client logic in a goroutine
		go s.manageRelayRegistrationAndTunnels(*relayServerAddr, *hostID, localGrpcServerAddr)
	}

	log.Println("INFO: Starting Fyne application UI...")
	fyneWindow.ShowAndRun()

	log.Println("INFO: Fyne application has exited.")
	log.Println("INFO: Ensuring gRPC server is stopped...")
	grpcServer.GracefulStop()
	log.Println("INFO: gRPC server shutdown complete. Exiting application.")
}

// manageRelayRegistrationAndTunnels handles the connection to the relay server,
// registers the host, and processes CREATE_TUNNEL commands.
func (s *server) manageRelayRegistrationAndTunnels(relayCtrlAddr, hostID, localGrpcSvcAddr string) {
	var controlConn net.Conn
	var err error

	// Retry loop for connecting to relay control port
	for {
		log.Printf("INFO: [Relay] Attempting to connect to relay control server %s...", relayCtrlAddr)
		if relayStatusLabel != nil {
			relayStatusLabel.SetText(fmt.Sprintf("Relay: Connecting to %s as '%s'...", relayCtrlAddr, hostID))
		}

		controlConn, err = net.DialTimeout("tcp", relayCtrlAddr, 10*time.Second)
		if err != nil {
			log.Printf("WARN: [Relay] Failed to connect to relay control server %s: %v. Retrying in 10s...", relayCtrlAddr, err)
			if relayStatusLabel != nil {
				relayStatusLabel.SetText(fmt.Sprintf("Relay: Failed to connect. Retrying..."))
			}
			time.Sleep(10 * time.Second)
			continue
		}
		break // Connected
	}
	defer controlConn.Close()
	log.Printf("INFO: [Relay] Connected to relay control server: %s", controlConn.RemoteAddr())
	if relayStatusLabel != nil {
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Connected to %s. Registering as '%s'...", relayCtrlAddr, hostID))
	}

	// Register with the relay server
	registerCmd := fmt.Sprintf("REGISTER_HOST %s\n", hostID)
	_, err = fmt.Fprint(controlConn, registerCmd)
	if err != nil {
		log.Printf("ERROR: [Relay] Failed to send REGISTER_HOST command: %v", err)
		if relayStatusLabel != nil {
			relayStatusLabel.SetText(fmt.Sprintf("Relay: Registration failed."))
		}
		return // Cannot proceed
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
			// Attempt to reconnect
			controlConn.Close()                                                             // Close the old one explicitly
			go s.manageRelayRegistrationAndTunnels(relayCtrlAddr, hostID, localGrpcSvcAddr) // Recursive call to reconnect
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
		case "CREATE_TUNNEL": // CREATE_TUNNEL <relay_data_addr> <session_token>
			if len(parts) < 3 {
				log.Printf("ERROR: [Relay] Invalid CREATE_TUNNEL command: %s", response)
				continue
			}
			relayDataAddr := parts[1]
			sessionToken := parts[2]
			log.Printf("INFO: [Relay] Received CREATE_TUNNEL for session %s, data port %s", sessionToken, relayDataAddr)
			if relayStatusLabel != nil {
				relayStatusLabel.SetText(fmt.Sprintf("Relay: Client connecting via session %s...", sessionToken[:8]))
			}
			go s.handleHostSideTunnel(localGrpcSvcAddr, relayDataAddr, sessionToken)
		default:
			log.Printf("WARN: [Relay] Unknown command from relay server: %s", response)
		}
	}
}

// handleHostSideTunnel connects to the relay's data port and proxies to the local gRPC service.
func (s *server) handleHostSideTunnel(localGrpcServiceAddr, relayDataAddr, sessionToken string) {
	log.Printf("INFO: [Tunnel %s] Host-side: Attempting to connect to relay data port %s", sessionToken, relayDataAddr)
	hostProxyConn, err := net.DialTimeout("tcp", relayDataAddr, 10*time.Second)
	if err != nil {
		log.Printf("ERROR: [Tunnel %s] Host-side: Failed to connect to relay data port %s: %v", sessionToken, relayDataAddr, err)
		return
	}
	defer hostProxyConn.Close()
	log.Printf("INFO: [Tunnel %s] Host-side: Connected to relay data port: %s", sessionToken, hostProxyConn.RemoteAddr())

	// Identify this connection to the relay server
	identCmd := fmt.Sprintf("SESSION_TOKEN %s HOST_PROXY\n", sessionToken)
	_, err = fmt.Fprint(hostProxyConn, identCmd)
	if err != nil {
		log.Printf("ERROR: [Tunnel %s] Host-side: Failed to send session token identification: %v", sessionToken, err)
		return
	}
	log.Printf("INFO: [Tunnel %s] Host-side: Sent identification: %s", sessionToken, strings.TrimSpace(identCmd))

	// Connect to the local gRPC service
	log.Printf("INFO: [Tunnel %s] Host-side: Connecting to local gRPC service at %s", sessionToken, localGrpcServiceAddr)
	localServiceConn, err := net.DialTimeout("tcp", localGrpcServiceAddr, 5*time.Second)
	if err != nil {
		log.Printf("ERROR: [Tunnel %s] Host-side: Failed to connect to local gRPC service %s: %v", sessionToken, localGrpcServiceAddr, err)
		return
	}
	defer localServiceConn.Close()
	log.Printf("INFO: [Tunnel %s] Host-side: Connected to local gRPC service. Starting proxy.", sessionToken)
	if relayStatusLabel != nil { // Update UI briefly
		originalText := relayStatusLabel.Text
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Active session %s", sessionToken[:8]))
		go func() {
			time.Sleep(5 * time.Second)
			if relayStatusLabel != nil && relayStatusLabel.Text == fmt.Sprintf("Relay: Active session %s", sessionToken[:8]) {
				relayStatusLabel.SetText(originalText) // Restore previous status or "Waiting for clients"
			}
		}()
	}

	// Proxy traffic
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer hostProxyConn.Close() // Ensure other side knows
		defer localServiceConn.Close()
		written, errCopy := io.Copy(localServiceConn, hostProxyConn) // relay -> local gRPC
		if errCopy != nil && errCopy != io.EOF && !strings.Contains(errCopy.Error(), "use of closed network connection") {
			log.Printf("ERROR: [Tunnel %s] Host-side: Error copying from relay to local: %v (bytes: %d)", sessionToken, errCopy, written)
		} else {
			log.Printf("INFO: [Tunnel %s] Host-side: Finished copying from relay to local. Bytes: %d", sessionToken, written)
		}
	}()

	go func() {
		defer wg.Done()
		defer localServiceConn.Close() // Ensure other side knows
		defer hostProxyConn.Close()
		written, errCopy := io.Copy(hostProxyConn, localServiceConn) // local gRPC -> relay
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
		return nil, fmt.Errorf("failed to load server key pair from embedded data: %w", err)
	}
	clientCertPool := x509.NewCertPool()
	if !clientCertPool.AppendCertsFromPEM(clientCACertEmbed) {
		return nil, fmt.Errorf("failed to append client CA certificate from embedded data to pool")
	}
	config := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCertPool,
		MinVersion:   tls.VersionTLS12,
		ServerName:   "localhost",
	}
	return credentials.NewTLS(config), nil
}

func (s *server) Ping(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{ClientTimestampNano: req.GetClientTimestampNano()}, nil
}

// Implement other service methods as needed...

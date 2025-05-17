package main

import (
	"bufio"
	"context"
	"crypto/rand" // For random ID generation
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/hex" // For random ID generation
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os" // Required for os.Stdout
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
	portFlag           = flag.Int("port", 32212, "The server port for direct gRPC connections")
	enableRelay        = flag.Bool("relay", false, "Enable relay mode to connect through a relay server")
	relayServerAddr    = flag.String("relayServer", "localhost:34000", "Address of the relay server's control port (IP:PORT)")
	hostIDFlag         = flag.String("hostID", "auto", "Unique ID for this host if not using relay, or as a suggestion. 'auto' for random.")
	fyneApp            fyne.App
	fyneWindow         fyne.Window // Made global for title updates
	serverStatusLabel  *widget.Label
	relayStatusLabel   *widget.Label
	hostIDDisplayLabel *widget.Label
)

const effectiveHostIDPrefix = "EFFECTIVE_HOST_ID:" // Prefix for launcher to detect the final Host ID

// generateRandomHostID creates a random alphanumeric string.
func generateRandomHostID(byteLength int) string {
	bytes := make([]byte, byteLength)
	if _, err := rand.Read(bytes); err != nil {
		log.Printf("WARN: Could not generate crypto/rand bytes for Host ID: %v. Using timestamp fallback.", err)
		return fmt.Sprintf("%08d", time.Now().UnixNano()%100000000)
	}
	return hex.EncodeToString(bytes)
}

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	initialHostID := *hostIDFlag
	if strings.ToLower(initialHostID) == "auto" || initialHostID == "" {
		initialHostID = generateRandomHostID(4) // Generate an 8-character hex ID
		log.Printf("INFO: Auto-generated initial Host ID: %s", initialHostID)
	} else {
		log.Printf("INFO: Using provided initial Host ID: %s", initialHostID)
	}

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
	fyneWindow = fyneApp.NewWindow(fmt.Sprintf("gRPC Server - Initializing...")) // Initial title

	serverStatusLabel = widget.NewLabel(fmt.Sprintf("Direct gRPC: Listening on %s", localGrpcServerAddr))
	serverStatusLabel.Alignment = fyne.TextAlignCenter

	hostIDDisplayLabel = widget.NewLabel("Determining Host ID...")
	hostIDDisplayLabel.Wrapping = fyne.TextWrapWord
	hostIDDisplayLabel.Alignment = fyne.TextAlignCenter

	relayStatusLabel = widget.NewLabel("Relay: Disabled")
	relayStatusLabel.Alignment = fyne.TextAlignCenter

	if *enableRelay {
		hostIDDisplayLabel.SetText("Registering with Relay server...")
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Connecting to %s...", *relayServerAddr))
	} else {
		// Not using relay, so initialHostID is the effective ID.
		hostIDDisplayLabel.SetText(fmt.Sprintf("Your Host ID: %s\n(Share this for direct connection)", initialHostID))
		fyneWindow.SetTitle(fmt.Sprintf("gRPC Server - Host ID: %s (Direct)", initialHostID))
		// Print effective host ID for launcher
		fmt.Fprintf(os.Stdout, "%s%s\n", effectiveHostIDPrefix, initialHostID)
		log.Printf("INFO: Effective Host ID (direct): %s", initialHostID)
	}

	quitButton := widget.NewButton("Shutdown Server", func() {
		log.Println("INFO: Shutdown button clicked. Stopping server...")
		grpcServer.GracefulStop()
		fyneApp.Quit()
	})

	fyneWindow.SetContent(container.NewVBox(
		hostIDDisplayLabel,
		serverStatusLabel,
		relayStatusLabel,
		quitButton,
	))
	fyneWindow.Resize(fyne.NewSize(500, 250))

	fyneWindow.SetOnClosed(func() {
		log.Println("INFO: Fyne window closed by user. Initiating server shutdown...")
		go grpcServer.GracefulStop()
		log.Println("INFO: Server shutdown process initiated from OnClosed.")
	})

	go func() {
		log.Printf("INFO: gRPC Server starting for direct connections at %s", localGrpcServerAddr)
		if err := grpcServer.Serve(localGrpcListener); err != nil {
			log.Printf("ERROR: Failed to serve direct gRPC: %v", err)
			if fyneApp != nil && serverStatusLabel != nil {
				fyneApp.SendNotification(&fyne.Notification{
					Title:   "gRPC Server Error",
					Content: fmt.Sprintf("Failed to serve direct gRPC: %v", err),
				})
				serverStatusLabel.SetText(fmt.Sprintf("Direct gRPC: Error - %v", err))
			}
		}
		log.Println("INFO: Direct gRPC Server has stopped.")
	}()

	if *enableRelay {
		// Pass initialHostID for logging purposes before relay assigns one.
		go s.manageRelayRegistrationAndTunnels(*relayServerAddr, initialHostID, localGrpcServerAddr)
	}

	log.Println("INFO: Starting Fyne application UI...")
	fyneWindow.ShowAndRun()

	log.Println("INFO: Fyne application has exited.")
	log.Println("INFO: Ensuring gRPC server is stopped...")
	grpcServer.GracefulStop()
	log.Println("INFO: gRPC server shutdown complete. Exiting application.")
}

// manageRelayRegistrationAndTunnels handles connection and communication with the relay server.
// localInitialIDHint is used for logging before the relay assigns an ID.
func (s *server) manageRelayRegistrationAndTunnels(relayCtrlAddrFull, localInitialIDHint, localGrpcSvcAddr string) {
	var controlConn net.Conn
	var err error
	var currentRegisteredRelayID string // Stores the ID assigned and confirmed by the relay server

	for { // Connection loop
		log.Printf("INFO: [Relay] Attempting to connect to relay control server %s (local ID hint: '%s')...", relayCtrlAddrFull, localInitialIDHint)
		if relayStatusLabel != nil {
			relayStatusLabel.SetText(fmt.Sprintf("Relay: Connecting to %s...", relayCtrlAddrFull))
			relayStatusLabel.Refresh()
		}

		controlConn, err = net.DialTimeout("tcp", relayCtrlAddrFull, 10*time.Second)
		if err != nil {
			log.Printf("WARN: [Relay] Failed to connect to relay control server %s: %v. Retrying in 10s...", relayCtrlAddrFull, err)
			if relayStatusLabel != nil {
				relayStatusLabel.SetText("Relay: Connection failed. Retrying...")
				relayStatusLabel.Refresh()
			}
			time.Sleep(10 * time.Second)
			continue
		}
		log.Printf("INFO: [Relay] Connected to relay control server: %s", controlConn.RemoteAddr())
		break // Successfully connected
	}
	defer controlConn.Close()

	// Register with the relay server - send no ID, relay will assign one.
	registerCmd := "REGISTER_HOST\n"
	_, err = fmt.Fprint(controlConn, registerCmd)
	if err != nil {
		log.Printf("ERROR: [Relay] Failed to send REGISTER_HOST command: %v", err)
		if relayStatusLabel != nil {
			relayStatusLabel.SetText("Relay: Registration command failed.")
			relayStatusLabel.Refresh()
		}
		// Optionally, could trigger a reconnect here by returning and letting main loop restart goroutine,
		// or implement retry logic within this function. For now, it exits the goroutine.
		return
	}
	log.Printf("INFO: [Relay] Sent: %s", strings.TrimSpace(registerCmd))
	if relayStatusLabel != nil {
		relayStatusLabel.SetText("Relay: Sent registration. Waiting for ID...")
		relayStatusLabel.Refresh()
	}

	reader := bufio.NewReader(controlConn)
	for { // Message processing loop
		response, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				log.Printf("INFO: [Relay] Control connection to relay server closed (EOF) for Host ID '%s'. Reconnecting...", currentRegisteredRelayID)
			} else {
				log.Printf("ERROR: [Relay] Error reading from relay control connection for Host ID '%s': %v. Reconnecting...", currentRegisteredRelayID, err)
			}
			controlConn.Close()
			// Re-initiate the connection and registration process
			go s.manageRelayRegistrationAndTunnels(relayCtrlAddrFull, localInitialIDHint, localGrpcSvcAddr)
			return // Exit this goroutine, new one will take over
		}

		response = strings.TrimSpace(response)
		log.Printf("INFO: [Relay] Received from relay (current/potential Host ID '%s'): %s", currentRegisteredRelayID, response)
		parts := strings.Fields(response)
		if len(parts) == 0 {
			continue
		}
		command := parts[0]

		switch command {
		case "HOST_REGISTERED":
			if len(parts) < 2 {
				log.Printf("ERROR: [Relay] Invalid HOST_REGISTERED response: %s", response)
				// Potentially disconnect and retry registration
				if relayStatusLabel != nil {
					relayStatusLabel.SetText("Relay: Invalid registration response.")
					relayStatusLabel.Refresh()
				}
				continue
			}
			assignedID := parts[1]
			currentRegisteredRelayID = assignedID // This is the authoritative ID from the relay
			log.Printf("INFO: [Relay] Successfully registered with relay server. Assigned Host ID: %s", currentRegisteredRelayID)

			// Print effective host ID for launcher
			fmt.Fprintf(os.Stdout, "%s%s\n", effectiveHostIDPrefix, currentRegisteredRelayID)
			log.Printf("INFO: Effective Host ID (relay): %s", currentRegisteredRelayID)

			// Update Fyne UI with the relay-assigned ID
			if hostIDDisplayLabel != nil {
				hostIDDisplayLabel.SetText(fmt.Sprintf("Your Relay Host ID: %s\n(Share this with clients)", currentRegisteredRelayID))
				hostIDDisplayLabel.Refresh()
			}
			if fyneWindow != nil {
				fyneWindow.SetTitle(fmt.Sprintf("gRPC Server - Host ID: %s (Relay)", currentRegisteredRelayID))
				// fyneWindow.Canvas().Refresh(fyneWindow.Content()) // May not be needed for title
			}
			if relayStatusLabel != nil {
				relayStatusLabel.SetText(fmt.Sprintf("Relay: Registered as '%s'. Waiting...", currentRegisteredRelayID))
				relayStatusLabel.Refresh()
			}

		case "CREATE_TUNNEL":
			if len(parts) < 3 {
				log.Printf("ERROR: [Relay] Invalid CREATE_TUNNEL command for Host ID '%s': %s", currentRegisteredRelayID, response)
				continue
			}
			if currentRegisteredRelayID == "" {
				log.Printf("ERROR: [Relay] Received CREATE_TUNNEL before host ID was registered: %s", response)
				continue
			}
			dynamicPortStr := parts[1]
			sessionToken := parts[2]
			log.Printf("INFO: [Relay] Received CREATE_TUNNEL for Host ID '%s', session %s, relay dynamic port %s", currentRegisteredRelayID, sessionToken, dynamicPortStr)

			relayHostIP, _, err := net.SplitHostPort(relayCtrlAddrFull)
			if err != nil {
				log.Printf("ERROR: [Relay] Could not parse host IP from relayCtrlAddrFull '%s': %v", relayCtrlAddrFull, err)
				continue
			}
			relayDataAddrForHost := net.JoinHostPort(relayHostIP, dynamicPortStr)
			log.Printf("INFO: [Relay] Host '%s' will connect to relay data endpoint: %s for session %s", currentRegisteredRelayID, relayDataAddrForHost, sessionToken)

			if relayStatusLabel != nil {
				relayStatusLabel.SetText(fmt.Sprintf("Relay: Client connecting (ID: %s)...", currentRegisteredRelayID))
				relayStatusLabel.Refresh()
			}
			// Pass the confirmed currentRegisteredRelayID to the tunnel handler
			go s.handleHostSideTunnel(localGrpcSvcAddr, relayDataAddrForHost, sessionToken, currentRegisteredRelayID)
		default:
			log.Printf("WARN: [Relay] Unknown command from relay server for Host ID '%s': %s", currentRegisteredRelayID, response)
		}
	}
}

// handleHostSideTunnel now uses registeredHostID for logging and UI updates.
func (s *server) handleHostSideTunnel(localGrpcServiceAddr, relayDataAddrForHost, sessionToken, registeredHostID string) {
	log.Printf("INFO: [Tunnel %s Host %s] Host-side: Attempting to connect to relay data endpoint %s", sessionToken, registeredHostID, relayDataAddrForHost)
	hostProxyConn, err := net.DialTimeout("tcp", relayDataAddrForHost, 10*time.Second)
	if err != nil {
		log.Printf("ERROR: [Tunnel %s Host %s] Host-side: Failed to connect to relay data endpoint %s: %v", sessionToken, registeredHostID, relayDataAddrForHost, err)
		return
	}
	defer hostProxyConn.Close()
	log.Printf("INFO: [Tunnel %s Host %s] Host-side: Connected to relay data endpoint: %s", sessionToken, registeredHostID, hostProxyConn.RemoteAddr())

	identCmd := fmt.Sprintf("SESSION_TOKEN %s HOST_PROXY\n", sessionToken)
	_, err = fmt.Fprint(hostProxyConn, identCmd)
	if err != nil {
		log.Printf("ERROR: [Tunnel %s Host %s] Host-side: Failed to send session token identification: %v", sessionToken, registeredHostID, err)
		return
	}
	log.Printf("INFO: [Tunnel %s Host %s] Host-side: Sent identification: %s", sessionToken, registeredHostID, strings.TrimSpace(identCmd))

	log.Printf("INFO: [Tunnel %s Host %s] Host-side: Connecting to local gRPC service at %s", sessionToken, registeredHostID, localGrpcServiceAddr)
	localServiceConn, err := net.DialTimeout("tcp", localGrpcServiceAddr, 5*time.Second)
	if err != nil {
		log.Printf("ERROR: [Tunnel %s Host %s] Host-side: Failed to connect to local gRPC service %s: %v", sessionToken, registeredHostID, localGrpcServiceAddr, err)
		return
	}
	defer localServiceConn.Close()
	log.Printf("INFO: [Tunnel %s Host %s] Host-side: Connected to local gRPC service. Starting proxy.", sessionToken, registeredHostID)

	originalRelayStatusText := ""
	if relayStatusLabel != nil {
		originalRelayStatusText = relayStatusLabel.Text // Save current text
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Active session (ID: %s)", registeredHostID))
		relayStatusLabel.Refresh()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer hostProxyConn.Close()
		defer localServiceConn.Close()
		written, errCopy := io.Copy(localServiceConn, hostProxyConn)
		logContext := fmt.Sprintf("[Tunnel %s Host %s]", sessionToken, registeredHostID)
		if errCopy != nil && !isNetworkCloseError(errCopy) {
			log.Printf("ERROR: %s Host-side: Error copying from relay to local: %v (bytes: %d)", logContext, errCopy, written)
		} else {
			log.Printf("INFO: %s Host-side: Finished copying from relay to local. Bytes: %d. Error (if any): %v", logContext, written, errCopy)
		}
	}()
	go func() {
		defer wg.Done()
		defer localServiceConn.Close()
		defer hostProxyConn.Close()
		written, errCopy := io.Copy(hostProxyConn, localServiceConn)
		logContext := fmt.Sprintf("[Tunnel %s Host %s]", sessionToken, registeredHostID)
		if errCopy != nil && !isNetworkCloseError(errCopy) {
			log.Printf("ERROR: %s Host-side: Error copying from local to relay: %v (bytes: %d)", logContext, errCopy, written)
		} else {
			log.Printf("INFO: %s Host-side: Finished copying from local to relay. Bytes: %d. Error (if any): %v", logContext, written, errCopy)
		}
	}()
	wg.Wait()
	log.Printf("INFO: [Tunnel %s Host %s] Host-side: Proxying finished.", sessionToken, registeredHostID)

	// Restore relay status label after session ends
	if relayStatusLabel != nil {
		// Check if the text is still "Active session..." before changing it back
		// This avoids overriding a newer status (e.g., "Reconnecting...")
		if strings.HasPrefix(relayStatusLabel.Text, "Relay: Active session") {
			if originalRelayStatusText != "" && !strings.HasPrefix(originalRelayStatusText, "Relay: Active session") {
				relayStatusLabel.SetText(originalRelayStatusText) // Restore previous specific text if sensible
			} else {
				// Default back to generic registered state
				relayStatusLabel.SetText(fmt.Sprintf("Relay: Registered as '%s'. Waiting...", registeredHostID))
			}
			relayStatusLabel.Refresh()
		}
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
		ServerName:   "localhost",
	}
	return credentials.NewTLS(config), nil
}

func (s *server) Ping(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{ClientTimestampNano: req.GetClientTimestampNano()}, nil
}

// isNetworkCloseError checks if the error is a common network connection closed error.
func isNetworkCloseError(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		return true
	}
	s := err.Error()
	// Add more specific error checks if needed, e.g., for specific OS network errors
	return strings.Contains(s, "use of closed network connection") ||
		strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "forcibly closed") // Windows specific
}

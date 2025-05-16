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
	hostIDFlag         = flag.String("hostID", "auto", "Unique ID for this host. Set to 'auto' or empty to auto-generate.") // Default to auto
	fyneApp            fyne.App
	serverStatusLabel  *widget.Label
	relayStatusLabel   *widget.Label
	hostIDDisplayLabel *widget.Label // For displaying the Host ID in server's own UI
)

const hostIDReadyPrefix = "HOST_ID_READY:" // Prefix for launcher to detect the Host ID

// generateRandomHostID creates a random alphanumeric string.
// byteLength determines the number of random bytes; the resulting hex string will be twice as long.
// For an 8-character ID, use byteLength = 4.
func generateRandomHostID(byteLength int) string {
	bytes := make([]byte, byteLength)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to a less random but still unique-ish ID if crypto/rand fails
		log.Printf("WARN: Could not generate crypto/rand bytes for Host ID: %v. Using timestamp fallback.", err)
		// Generate an 8-digit number from timestamp
		return fmt.Sprintf("%08d", time.Now().UnixNano()%100000000)
	}
	return hex.EncodeToString(bytes) // Example: 4 bytes -> 8 hex characters
}

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Determine Host ID
	actualHostID := *hostIDFlag
	if strings.ToLower(actualHostID) == "auto" || actualHostID == "" {
		actualHostID = generateRandomHostID(4) // Generate an 8-character hex ID
		log.Printf("INFO: Auto-generated Host ID: %s", actualHostID)
	} else {
		log.Printf("INFO: Using provided Host ID: %s", actualHostID)
	}

	// Print the Host ID to stdout for the launcher to capture.
	// This should be one of the first things printed.
	fmt.Printf("%s%s\n", hostIDReadyPrefix, actualHostID)
	// Ensure it's flushed, especially if server might not print much else immediately.
	// However, fmt.Printf with \n usually flushes on line-buffered terminals.
	// For robustness with pipes, an explicit flush might be considered if issues arise,
	// but often not needed for simple line-based output.

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
	// Update window title to include the actual Host ID
	fyneWindow := fyneApp.NewWindow(fmt.Sprintf("gRPC Server - Host ID: %s", actualHostID))

	serverStatusLabel = widget.NewLabel(fmt.Sprintf("Direct gRPC: Listening on %s", localGrpcServerAddr))
	serverStatusLabel.Alignment = fyne.TextAlignCenter

	// Label to display the Host ID in the server's UI
	hostIDDisplayLabel = widget.NewLabel(fmt.Sprintf("Your Host ID: %s\n(Share this with clients for relay connection)", actualHostID))
	hostIDDisplayLabel.Wrapping = fyne.TextWrapWord // Wrap text if ID is long or window is small
	hostIDDisplayLabel.Alignment = fyne.TextAlignCenter

	relayStatusLabel = widget.NewLabel("Relay: Disabled")
	if *enableRelay {
		// Use the actualHostID when displaying status and connecting to relay
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Connecting to %s as '%s'...", *relayServerAddr, actualHostID))
	}
	relayStatusLabel.Alignment = fyne.TextAlignCenter

	quitButton := widget.NewButton("Shutdown Server", func() {
		log.Println("INFO: Shutdown button clicked. Stopping server...")
		grpcServer.GracefulStop()
		fyneApp.Quit()
	})

	fyneWindow.SetContent(container.NewVBox(
		hostIDDisplayLabel, // Display the Host ID prominently
		serverStatusLabel,
		relayStatusLabel,
		quitButton,
	))
	fyneWindow.Resize(fyne.NewSize(500, 250)) // Adjusted size for new label
	// fyneWindow.SetFixedSize(true) // Consider allowing resize if Host ID is long

	fyneWindow.SetOnClosed(func() {
		log.Println("INFO: Fyne window closed by user. Initiating server shutdown...")
		go grpcServer.GracefulStop() // Ensure gRPC server stops
		log.Println("INFO: Server shutdown process initiated from OnClosed.")
	})

	go func() {
		log.Printf("INFO: gRPC Server starting for direct connections at %s", localGrpcServerAddr)
		if err := grpcServer.Serve(localGrpcListener); err != nil {
			log.Printf("ERROR: Failed to serve direct gRPC: %v", err)
			if fyneApp != nil && serverStatusLabel != nil { // Check if UI elements are initialized
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
		// Pass the determined actualHostID to the relay management function
		go s.manageRelayRegistrationAndTunnels(*relayServerAddr, actualHostID, localGrpcServerAddr)
	}

	log.Println("INFO: Starting Fyne application UI...")
	fyneWindow.ShowAndRun()

	log.Println("INFO: Fyne application has exited.")
	log.Println("INFO: Ensuring gRPC server is stopped...")
	grpcServer.GracefulStop() // Final attempt to stop
	log.Println("INFO: gRPC server shutdown complete. Exiting application.")
}

// manageRelayRegistrationAndTunnels now uses the actualHostID passed to it.
func (s *server) manageRelayRegistrationAndTunnels(relayCtrlAddrFull, actualHostID, localGrpcSvcAddr string) {
	var controlConn net.Conn
	var err error

	for {
		log.Printf("INFO: [Relay] Attempting to connect to relay control server %s for Host ID '%s'...", relayCtrlAddrFull, actualHostID)
		if relayStatusLabel != nil {
			relayStatusLabel.SetText(fmt.Sprintf("Relay: Connecting to %s as '%s'...", relayCtrlAddrFull, actualHostID))
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
		break // Successfully connected
	}
	defer controlConn.Close()
	log.Printf("INFO: [Relay] Connected to relay control server: %s", controlConn.RemoteAddr())
	if relayStatusLabel != nil {
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Connected to %s. Registering as '%s'...", relayCtrlAddrFull, actualHostID))
	}

	registerCmd := fmt.Sprintf("REGISTER_HOST %s\n", actualHostID) // Use the actualHostID
	_, err = fmt.Fprint(controlConn, registerCmd)
	if err != nil {
		log.Printf("ERROR: [Relay] Failed to send REGISTER_HOST command for ID '%s': %v", actualHostID, err)
		if relayStatusLabel != nil {
			relayStatusLabel.SetText(fmt.Sprintf("Relay: Registration failed for ID %s.", actualHostID))
		}
		return // Cannot proceed if registration fails
	}
	log.Printf("INFO: [Relay] Sent: %s", strings.TrimSpace(registerCmd))

	reader := bufio.NewReader(controlConn)
	for {
		response, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				log.Printf("INFO: [Relay] Control connection to relay server closed (EOF) for Host ID '%s'. Reconnecting...", actualHostID)
			} else {
				log.Printf("ERROR: [Relay] Error reading from relay control connection for Host ID '%s': %v. Reconnecting...", actualHostID, err)
			}
			controlConn.Close() // Close the old connection before retrying
			// Re-initiate the connection and registration process
			go s.manageRelayRegistrationAndTunnels(relayCtrlAddrFull, actualHostID, localGrpcSvcAddr)
			return // Exit this goroutine, new one will take over
		}

		response = strings.TrimSpace(response)
		log.Printf("INFO: [Relay] Received from relay for Host ID '%s': %s", actualHostID, response)
		parts := strings.Fields(response)
		if len(parts) == 0 {
			continue
		}
		command := parts[0]

		switch command {
		case "HOST_REGISTERED":
			if len(parts) > 1 && parts[1] == actualHostID {
				log.Printf("INFO: [Relay] Successfully registered with relay server as Host ID: %s", parts[1])
				if relayStatusLabel != nil {
					relayStatusLabel.SetText(fmt.Sprintf("Relay: Registered as '%s'. Waiting for clients.", actualHostID))
				}
			} else {
				log.Printf("WARN: [Relay] Received HOST_REGISTERED for an unexpected ID. Expected: %s, Got response: %s", actualHostID, response)
			}
		case "CREATE_TUNNEL":
			if len(parts) < 3 {
				log.Printf("ERROR: [Relay] Invalid CREATE_TUNNEL command for Host ID '%s': %s", actualHostID, response)
				continue
			}
			dynamicPortStr := parts[1]
			sessionToken := parts[2]
			log.Printf("INFO: [Relay] Received CREATE_TUNNEL for Host ID '%s', session %s, relay dynamic port %s", actualHostID, sessionToken, dynamicPortStr)

			relayHostIP, _, err := net.SplitHostPort(relayCtrlAddrFull)
			if err != nil {
				log.Printf("ERROR: [Relay] Could not parse host IP from relayCtrlAddrFull '%s': %v", relayCtrlAddrFull, err)
				continue // Cannot proceed without relay host IP
			}
			relayDataAddrForHost := net.JoinHostPort(relayHostIP, dynamicPortStr)
			log.Printf("INFO: [Relay] Host '%s' will connect to relay data endpoint: %s for session %s", actualHostID, relayDataAddrForHost, sessionToken)

			if relayStatusLabel != nil {
				relayStatusLabel.SetText(fmt.Sprintf("Relay: Client connecting via session %s...", sessionToken[:8]))
			}
			// Pass actualHostID for better logging context in handleHostSideTunnel
			go s.handleHostSideTunnel(localGrpcSvcAddr, relayDataAddrForHost, sessionToken, actualHostID)
		default:
			log.Printf("WARN: [Relay] Unknown command from relay server for Host ID '%s': %s", actualHostID, response)
		}
	}
}

// handleHostSideTunnel now includes actualHostID for logging.
func (s *server) handleHostSideTunnel(localGrpcServiceAddr, relayDataAddrForHost, sessionToken, actualHostID string) {
	log.Printf("INFO: [Tunnel %s Host %s] Host-side: Attempting to connect to relay data endpoint %s", sessionToken, actualHostID, relayDataAddrForHost)
	hostProxyConn, err := net.DialTimeout("tcp", relayDataAddrForHost, 10*time.Second)
	if err != nil {
		log.Printf("ERROR: [Tunnel %s Host %s] Host-side: Failed to connect to relay data endpoint %s: %v", sessionToken, actualHostID, relayDataAddrForHost, err)
		return
	}
	defer hostProxyConn.Close()
	log.Printf("INFO: [Tunnel %s Host %s] Host-side: Connected to relay data endpoint: %s", sessionToken, actualHostID, hostProxyConn.RemoteAddr())

	identCmd := fmt.Sprintf("SESSION_TOKEN %s HOST_PROXY\n", sessionToken)
	_, err = fmt.Fprint(hostProxyConn, identCmd)
	if err != nil {
		log.Printf("ERROR: [Tunnel %s Host %s] Host-side: Failed to send session token identification: %v", sessionToken, actualHostID, err)
		return
	}
	log.Printf("INFO: [Tunnel %s Host %s] Host-side: Sent identification: %s", sessionToken, actualHostID, strings.TrimSpace(identCmd))

	log.Printf("INFO: [Tunnel %s Host %s] Host-side: Connecting to local gRPC service at %s", sessionToken, actualHostID, localGrpcServiceAddr)
	localServiceConn, err := net.DialTimeout("tcp", localGrpcServiceAddr, 5*time.Second)
	if err != nil {
		log.Printf("ERROR: [Tunnel %s Host %s] Host-side: Failed to connect to local gRPC service %s: %v", sessionToken, actualHostID, localGrpcServiceAddr, err)
		return
	}
	defer localServiceConn.Close()
	log.Printf("INFO: [Tunnel %s Host %s] Host-side: Connected to local gRPC service. Starting proxy.", sessionToken, actualHostID)
	if relayStatusLabel != nil {
		originalText := relayStatusLabel.Text // Save current text
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Active session %s", sessionToken[:8]))
		// Restore previous status after a delay
		go func() {
			time.Sleep(5 * time.Second) // Display active session for 5 seconds
			// Check if the label text is still the "Active session" one before changing
			if relayStatusLabel != nil && relayStatusLabel.Text == fmt.Sprintf("Relay: Active session %s", sessionToken[:8]) {
				// Restore to "Registered" or the original text if it was different
				// For simplicity, assume originalText was "Registered..." if it wasn't "Disabled" or "Connecting..."
				// A more robust way would be to manage server state.
				if strings.HasPrefix(originalText, "Relay: Registered") {
					relayStatusLabel.SetText(originalText)
				} else {
					relayStatusLabel.SetText(fmt.Sprintf("Relay: Registered as '%s'. Waiting for clients.", actualHostID))
				}
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
		logContext := fmt.Sprintf("[Tunnel %s Host %s]", sessionToken, actualHostID)
		if errCopy != nil && errCopy != io.EOF && !strings.Contains(errCopy.Error(), "use of closed network connection") {
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
		logContext := fmt.Sprintf("[Tunnel %s Host %s]", sessionToken, actualHostID)
		if errCopy != nil && errCopy != io.EOF && !strings.Contains(errCopy.Error(), "use of closed network connection") {
			log.Printf("ERROR: %s Host-side: Error copying from local to relay: %v (bytes: %d)", logContext, errCopy, written)
		} else {
			log.Printf("INFO: %s Host-side: Finished copying from local to relay. Bytes: %d. Error (if any): %v", logContext, written, errCopy)
		}
	}()
	wg.Wait()
	log.Printf("INFO: [Tunnel %s Host %s] Host-side: Proxying finished.", sessionToken, actualHostID)
	// After proxying, restore the relay status label if it was showing active session
	if relayStatusLabel != nil && relayStatusLabel.Text == fmt.Sprintf("Relay: Active session %s", sessionToken[:8]) {
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Registered as '%s'. Waiting for clients.", actualHostID))
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
		ServerName:   "localhost", // ServerName for server's own TLS config
	}
	return credentials.NewTLS(config), nil
}

func (s *server) Ping(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{ClientTimestampNano: req.GetClientTimestampNano()}, nil
}

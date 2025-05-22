package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	pb "control_grpc/gen/proto"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"golang.org/x/crypto/bcrypt"
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
	pb.UnimplementedTerminalServiceServer

	localGrpcAddr       string
	sessionPasswordHash string
	currentRelayHostID  string
	grpcServer          *grpc.Server
}

var (
	portFlag            = flag.Int("port", 32212, "The server port for direct gRPC connections")
	enableRelay         = flag.Bool("relay", false, "Enable relay mode to connect through a relay server")
	relayServerAddr     = flag.String("relayServer", "localhost:34000", "Address of the relay server's control port (IP:PORT)")
	hostIDFlag          = flag.String("hostID", "auto", "Unique ID for this host. 'auto' for random generation.")
	sessionPasswordFlag = flag.String("sessionPassword", "", "HASHED password to protect this host session when using relay (optional).")

	fyneApp             fyne.App
	fyneWindow          fyne.Window
	serverStatusLabel   *widget.Label
	relayStatusLabel    *widget.Label
	hostIDDisplayLabel  *widget.Label
	passwordStatusLabel *widget.Label
)

const effectiveHostIDPrefix = "EFFECTIVE_HOST_ID:"
const shutdownTimeout = 5 * time.Second

func generateRandomHostID(byteLength int) string {
	bytes := make([]byte, byteLength)
	if _, err := rand.Read(bytes); err != nil {
		log.Printf("WARN: Could not generate crypto/rand bytes for Host ID: %v. Using timestamp fallback.", err)
		return fmt.Sprintf("randfail%08d", time.Now().UnixNano()%100000000)
	}
	return hex.EncodeToString(bytes)
}

func tryGracefulShutdown(s *server, timeout time.Duration) bool {
	if s.grpcServer == nil {
		log.Println("INFO: gRPC server instance is nil, no shutdown needed or already stopped.")
		return false
	}

	log.Println("INFO: Initiating graceful shutdown of gRPC server...")
	stopped := make(chan struct{})
	go func() {
		s.grpcServer.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
		log.Println("INFO: gRPC server gracefully stopped.")
	case <-time.After(timeout):
		log.Printf("WARN: Graceful shutdown timed out after %v. Forcing stop.", timeout)
		s.grpcServer.Stop()
		log.Println("INFO: gRPC server forcefully stopped.")
	}
	return true
}

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	initialHostID := *hostIDFlag
	if strings.ToLower(initialHostID) == "auto" || initialHostID == "" {
		initialHostID = generateRandomHostID(4)
		log.Printf("INFO: Auto-generated initial Host ID: %s", initialHostID)
	} else {
		log.Printf("INFO: Using provided initial Host ID: %s", initialHostID)
	}

	s := &server{
		sessionPasswordHash: *sessionPasswordFlag,
	}
	if s.sessionPasswordHash != "" {
		log.Printf("INFO: Session password protection is ENABLED.")
	} else {
		log.Printf("INFO: Session password protection is DISABLED.")
	}

	localGrpcListener, err := net.Listen("tcp", fmt.Sprintf(":%d", *portFlag))
	if err != nil {
		log.Fatalf("FATAL: Failed to listen on port %d: %v", *portFlag, err)
	}
	s.localGrpcAddr = localGrpcListener.Addr().String()
	log.Printf("INFO: Local gRPC server will listen on %s", s.localGrpcAddr)

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
	s.grpcServer = grpcServer

	pb.RegisterAuthServiceServer(grpcServer, s)
	pb.RegisterRemoteControlServiceServer(grpcServer, s)
	pb.RegisterFileTransferServiceServer(grpcServer, s)
	pb.RegisterTerminalServiceServer(grpcServer, s)
	reflection.Register(grpcServer)

	fyneApp = app.New()
	fyneWindow = fyneApp.NewWindow("gRPC Server - Initializing...")

	serverStatusLabel = widget.NewLabel(fmt.Sprintf("Direct gRPC: Listening on %s", s.localGrpcAddr))
	serverStatusLabel.Alignment = fyne.TextAlignCenter
	hostIDDisplayLabel = widget.NewLabel("Determining Host ID...")
	hostIDDisplayLabel.Wrapping = fyne.TextWrapWord
	hostIDDisplayLabel.Alignment = fyne.TextAlignCenter
	passwordStatusText := "Password: None"
	if s.sessionPasswordHash != "" {
		passwordStatusText = "Password: Set (Protected)"
	}
	passwordStatusLabel = widget.NewLabel(passwordStatusText)
	passwordStatusLabel.Alignment = fyne.TextAlignCenter
	relayStatusLabel = widget.NewLabel("Relay: Disabled")
	relayStatusLabel.Alignment = fyne.TextAlignCenter

	if *enableRelay {
		hostIDDisplayLabel.SetText("Registering with Relay server...")
		relayStatusLabel.SetText(fmt.Sprintf("Relay: Connecting to %s...", *relayServerAddr))
	} else {
		s.currentRelayHostID = initialHostID
		hostIDDisplayLabel.SetText(fmt.Sprintf("Your Host ID: %s\n(Share this for direct connection)", s.currentRelayHostID))
		fyneWindow.SetTitle(fmt.Sprintf("gRPC Server - Host ID: %s (Direct)", s.currentRelayHostID))
		fmt.Fprintf(os.Stdout, "%s%s\n", effectiveHostIDPrefix, s.currentRelayHostID)
		log.Printf("INFO: Effective Host ID (direct mode): %s", s.currentRelayHostID)
	}

	quitButton := widget.NewButton("Shutdown Server", func() {
		log.Println("INFO: Shutdown button clicked. Initiating server shutdown sequence...")

		serverStatusLabel.SetText("Server shutting down...")

		if tryGracefulShutdown(s, shutdownTimeout) {

		}
		log.Println("INFO: Quitting Fyne application via button.")
		fyneApp.Quit()
	})

	fyneWindow.SetContent(container.NewVBox(
		hostIDDisplayLabel,
		passwordStatusLabel,
		serverStatusLabel,
		relayStatusLabel,
		quitButton,
	))
	fyneWindow.Resize(fyne.NewSize(500, 280))

	fyneWindow.SetOnClosed(func() {
		log.Println("INFO: Fyne window closed by user. Initiating server shutdown sequence...")

		tryGracefulShutdown(s, shutdownTimeout)

		log.Println("INFO: Server shutdown process initiated from OnClosed. Fyne app will now quit.")
	})

	go func() {
		log.Printf("INFO: gRPC Server starting for direct connections at %s", s.localGrpcAddr)
		if err := grpcServer.Serve(localGrpcListener); err != nil {

			log.Printf("INFO: grpcServer.Serve completed/exited: %v", err)
			if fyneApp != nil && serverStatusLabel != nil && !strings.Contains(err.Error(), "closed") {

				fyneApp.SendNotification(&fyne.Notification{
					Title:   "gRPC Server Error",
					Content: fmt.Sprintf("gRPC server issue: %v", err),
				})
				serverStatusLabel.SetText(fmt.Sprintf("Direct gRPC: Error - %v", err))
			}
		}
		log.Println("INFO: Direct gRPC Server Serve goroutine has finished.")
	}()

	if *enableRelay {
		go s.manageRelayRegistrationAndTunnels(*relayServerAddr, initialHostID, s.localGrpcAddr)
	}

	log.Println("INFO: Starting Fyne application UI...")
	fyneWindow.ShowAndRun()

	log.Println("INFO: Fyne application has exited.")
	log.Println("INFO: Performing final server stop...")
	if s.grpcServer != nil {
		s.grpcServer.Stop()
		log.Println("INFO: Final grpcServer.Stop() called.")
	}
	log.Println("INFO: Server shutdown complete. Exiting application.")
	os.Exit(0)
}

func (s *server) manageRelayRegistrationAndTunnels(relayCtrlAddrFull, localInitialIDHint, localGrpcSvcAddr string) {
	var controlConn net.Conn
	var err error
	for {
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
		break
	}
	defer controlConn.Close()

	registerCmd := fmt.Sprintf("REGISTER_HOST %s\n", localInitialIDHint)
	_, err = fmt.Fprint(controlConn, registerCmd)
	if err != nil {
		log.Printf("ERROR: [Relay] Failed to send REGISTER_HOST command: %v", err)
		if relayStatusLabel != nil {
			relayStatusLabel.SetText("Relay: Registration command failed.")
			relayStatusLabel.Refresh()
		}
		return
	}
	log.Printf("INFO: [Relay] Sent: %s", strings.TrimSpace(registerCmd))
	if relayStatusLabel != nil {
		relayStatusLabel.SetText("Relay: Sent registration. Waiting for ID...")
		relayStatusLabel.Refresh()
	}

	reader := bufio.NewReader(controlConn)
	for {
		response, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				log.Printf("INFO: [Relay] Control connection to relay server closed (EOF) for Host ID '%s'. Reconnecting...", s.currentRelayHostID)
			} else {
				log.Printf("ERROR: [Relay] Error reading from relay control connection for Host ID '%s': %v. Reconnecting...", s.currentRelayHostID, err)
			}
			controlConn.Close()
			go s.manageRelayRegistrationAndTunnels(relayCtrlAddrFull, localInitialIDHint, localGrpcSvcAddr)
			return
		}

		response = strings.TrimSpace(response)
		log.Printf("INFO: [Relay] Received from relay (current/potential Host ID '%s'): %s", s.currentRelayHostID, response)
		parts := strings.Fields(response)
		if len(parts) == 0 {
			continue
		}
		command := parts[0]

		switch command {
		case "HOST_REGISTERED":
			if len(parts) < 2 {
				log.Printf("ERROR: [Relay] Invalid HOST_REGISTERED response: %s", response)
				continue
			}
			assignedID := parts[1]
			s.currentRelayHostID = assignedID
			log.Printf("INFO: [Relay] Successfully registered with relay server. Assigned Host ID: %s", s.currentRelayHostID)
			fmt.Fprintf(os.Stdout, "%s%s\n", effectiveHostIDPrefix, s.currentRelayHostID)
			log.Printf("INFO: Effective Host ID (relay mode): %s", s.currentRelayHostID)

			if hostIDDisplayLabel != nil {
				hostIDDisplayLabel.SetText(fmt.Sprintf("Your Relay Host ID: %s\n(Share this with clients)", s.currentRelayHostID))
				hostIDDisplayLabel.Refresh()
			}
			if fyneWindow != nil {
				fyneWindow.SetTitle(fmt.Sprintf("gRPC Server - Host ID: %s (Relay)", s.currentRelayHostID))
			}
			if relayStatusLabel != nil {
				relayStatusLabel.SetText(fmt.Sprintf("Relay: Registered as '%s'. Waiting for clients...", s.currentRelayHostID))
				relayStatusLabel.Refresh()
			}

		case "VERIFY_PASSWORD_REQUEST":
			log.Printf("DEBUG: [Relay] Received VERIFY_PASSWORD_REQUEST: %s", response)
			var plainTextPasswordAttempt string
			requestToken := ""

			if len(parts) >= 2 {
				requestToken = parts[1]
			} else {
				log.Printf("ERROR: [Relay] Invalid VERIFY_PASSWORD_REQUEST (missing token): %s", response)
				continue
			}

			if len(parts) >= 3 {
				plainTextPasswordAttempt = strings.Join(parts[2:], " ")
			} else {
				plainTextPasswordAttempt = ""
				log.Printf("DEBUG: [Relay] No password string provided in VERIFY_PASSWORD_REQUEST for token %s.", requestToken)
			}
			log.Printf("DEBUG: [Relay] Token: '%s', Password Attempt (plain text): '%s', Stored Hash: '%s'", requestToken, plainTextPasswordAttempt, s.sessionPasswordHash)

			isValid := false
			if s.sessionPasswordHash == "" {
				log.Printf("INFO: [Relay] Password verification for token '%s'. Host has no password. Granting access.", requestToken)
				isValid = true
			} else {
				errCompare := bcrypt.CompareHashAndPassword([]byte(s.sessionPasswordHash), []byte(plainTextPasswordAttempt))
				if errCompare == nil {
					log.Printf("INFO: [Relay] Password verification for token '%s'. Password MATCHED.", requestToken)
					isValid = true
				} else {
					log.Printf("WARN: [Relay] Password verification for token '%s'. Password MISMATCH (attempt: '%s', err: %v). Denying access.", requestToken, plainTextPasswordAttempt, errCompare)
					isValid = false
				}
			}

			respCmd := fmt.Sprintf("VERIFY_PASSWORD_RESPONSE %s %t\n", requestToken, isValid)
			_, errSend := fmt.Fprint(controlConn, respCmd)
			if errSend != nil {
				log.Printf("ERROR: [Relay] Failed to send VERIFY_PASSWORD_RESPONSE for token %s: %v", requestToken, errSend)
			} else {
				log.Printf("INFO: [Relay] Sent to relay: %s", strings.TrimSpace(respCmd))
			}

		case "CREATE_TUNNEL":
			if len(parts) < 3 {
				log.Printf("ERROR: [Relay] Invalid CREATE_TUNNEL command for Host ID '%s': %s", s.currentRelayHostID, response)
				continue
			}
			if s.currentRelayHostID == "" {
				log.Printf("ERROR: [Relay] Received CREATE_TUNNEL before host ID was registered: %s. Ignoring.", response)
				continue
			}
			relayDynamicPortStr := parts[1]
			sessionToken := parts[2]
			log.Printf("INFO: [Relay] Received CREATE_TUNNEL for Host ID '%s', session token %s, relay dynamic port %s", s.currentRelayHostID, sessionToken, relayDynamicPortStr)

			relayHostIP, _, err := net.SplitHostPort(relayCtrlAddrFull)
			if err != nil {
				log.Printf("ERROR: [Relay] Could not parse host IP from relayCtrlAddrFull '%s': %v. Cannot create tunnel.", relayCtrlAddrFull, err)
				continue
			}
			relayDataAddrForHost := net.JoinHostPort(relayHostIP, relayDynamicPortStr)
			log.Printf("INFO: [Relay] Host '%s' will connect to relay data endpoint: %s for session %s", s.currentRelayHostID, relayDataAddrForHost, sessionToken)

			if relayStatusLabel != nil {
				relayStatusLabel.SetText(fmt.Sprintf("Relay: Client connecting (ID: %s, Session: %s)...", s.currentRelayHostID, sessionToken[:6]))
				relayStatusLabel.Refresh()
			}
			go s.handleHostSideTunnel(localGrpcSvcAddr, relayDataAddrForHost, sessionToken, s.currentRelayHostID)
		default:
			log.Printf("WARN: [Relay] Unknown command from relay server for Host ID '%s': %s", s.currentRelayHostID, response)
		}
	}
}

func (s *server) handleHostSideTunnel(localGrpcServiceAddr, relayDataAddrForHost, sessionToken, registeredHostID string) {
	logCtx := fmt.Sprintf("[Tunnel %s Host %s]", sessionToken[:6], registeredHostID)
	log.Printf("INFO: %s Host-side: Attempting to connect to relay data endpoint %s", logCtx, relayDataAddrForHost)

	hostProxyConn, err := net.DialTimeout("tcp", relayDataAddrForHost, 10*time.Second)
	if err != nil {
		log.Printf("ERROR: %s Host-side: Failed to connect to relay data endpoint %s: %v", logCtx, relayDataAddrForHost, err)
		return
	}
	defer hostProxyConn.Close()
	log.Printf("INFO: %s Host-side: Connected to relay data endpoint: %s", logCtx, hostProxyConn.RemoteAddr())

	identCmd := fmt.Sprintf("SESSION_TOKEN %s HOST_PROXY\n", sessionToken)
	_, err = fmt.Fprint(hostProxyConn, identCmd)
	if err != nil {
		log.Printf("ERROR: %s Host-side: Failed to send session token identification: %v", logCtx, err)
		return
	}
	log.Printf("INFO: %s Host-side: Sent identification: %s", logCtx, strings.TrimSpace(identCmd))

	log.Printf("INFO: %s Host-side: Connecting to local gRPC service at %s", logCtx, localGrpcServiceAddr)
	localServiceConn, err := net.DialTimeout("tcp", localGrpcServiceAddr, 5*time.Second)
	if err != nil {
		log.Printf("ERROR: %s Host-side: Failed to connect to local gRPC service %s: %v", logCtx, localGrpcServiceAddr, err)
		return
	}
	defer localServiceConn.Close()
	log.Printf("INFO: %s Host-side: Connected to local gRPC service. Starting bi-directional proxy.", logCtx)

	originalRelayStatusText := ""
	if relayStatusLabel != nil {
		originalRelayStatusText = relayStatusLabel.Text
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
		if errCopy != nil && !isNetworkCloseError(errCopy) {
			log.Printf("ERROR: %s Host-side: Error copying from relay to local: %v (bytes: %d)", logCtx, errCopy, written)
		} else {
			log.Printf("INFO: %s Host-side: Finished copying from relay to local. Bytes: %d. Error (if any): %v", logCtx, written, errCopy)
		}
	}()
	go func() {
		defer wg.Done()
		defer localServiceConn.Close()
		defer hostProxyConn.Close()
		written, errCopy := io.Copy(hostProxyConn, localServiceConn)
		if errCopy != nil && !isNetworkCloseError(errCopy) {
			log.Printf("ERROR: %s Host-side: Error copying from local to relay: %v (bytes: %d)", logCtx, errCopy, written)
		} else {
			log.Printf("INFO: %s Host-side: Finished copying from local to relay. Bytes: %d. Error (if any): %v", logCtx, written, errCopy)
		}
	}()
	wg.Wait()
	log.Printf("INFO: %s Host-side: Proxying finished. Tunnel closed.", logCtx)

	if relayStatusLabel != nil {
		if strings.Contains(relayStatusLabel.Text, fmt.Sprintf("Active session (ID: %s)", registeredHostID)) {
			if originalRelayStatusText != "" && !strings.HasPrefix(originalRelayStatusText, "Relay: Active session") {
				relayStatusLabel.SetText(originalRelayStatusText)
			} else {
				relayStatusLabel.SetText(fmt.Sprintf("Relay: Registered as '%s'. Waiting for clients...", registeredHostID))
			}
			relayStatusLabel.Refresh()
		}
	}
}

func loadTLSCredentialsFromEmbed() (credentials.TransportCredentials, error) {
	serverCert, err := tls.X509KeyPair(serverCertEmbed, serverKeyEmbed)
	if err != nil {
		return nil, fmt.Errorf("failed to load server key pair from embedded data: %w", err)
	}
	clientCertPool := x509.NewCertPool()
	if !clientCertPool.AppendCertsFromPEM(clientCACertEmbed) {
		return nil, fmt.Errorf("failed to append client CA cert to pool: %w", err)
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

func isNetworkCloseError(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "use of closed network connection") ||
		strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "forcibly closed")
}

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"flag" // Standard library flag package
	"fmt"
	"fyne.io/fyne/v2/dialog"
	"image"
	"io"
	"log"
	"net"
	"os" // Needed for os.Args and os.Exit
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	pb "control_grpc/gen/proto"
)

//go:embed client.crt
var clientCertEmbed []byte

//go:embed client.key
var clientKeyEmbed []byte

//go:embed server.crt
var serverCACertEmbed []byte

var (
	treeDataMutex       sync.RWMutex
	nodesMap            = make(map[string]*pb.FSNode)
	childrenMap         = make(map[string][]string)
	filesClient         pb.FileTransferServiceClient
	refreshTreeChan     = make(chan string, 1)
	mainWindow          fyne.Window
	mouseEvents         = make(chan *pb.FeedRequest, 120)
	pingLabel           *widget.Label
	fpsLabel            *widget.Label // New label for FPS
	remoteControlClient pb.RemoteControlServiceClient

	// Declare flag variables as pointers. They will be populated by the FlagSet.
	serverAddrActual *string // Renamed to avoid conflict if a package also defines 'serverAddr'
	connectionType   *string
	sessionToken     *string
)

// customRelayDialer is used by gRPC when connecting via relay.
// It establishes a raw TCP connection, sends the session token, then hands the connection to gRPC.
func customRelayDialer(ctx context.Context, targetRelayDataAddr string) (net.Conn, error) {
	log.Printf("INFO: [Relay Dialer] Attempting to dial relay data address: %s", targetRelayDataAddr)
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", targetRelayDataAddr)
	if err != nil {
		log.Printf("ERROR: [Relay Dialer] Failed to dial %s: %v", targetRelayDataAddr, err)
		return nil, fmt.Errorf("relay dialer failed to connect to %s: %w", targetRelayDataAddr, err)
	}
	log.Printf("INFO: [Relay Dialer] Connected to %s. Sending session token.", targetRelayDataAddr)

	if sessionToken == nil || *sessionToken == "" {
		conn.Close()
		log.Printf("ERROR: [Relay Dialer] Session token is not set.")
		return nil, fmt.Errorf("relay dialer session token not set")
	}
	identMsg := fmt.Sprintf("SESSION_TOKEN %s CLIENT_APP\n", *sessionToken)
	_, err = fmt.Fprint(conn, identMsg)
	if err != nil {
		conn.Close()
		log.Printf("ERROR: [Relay Dialer] Failed to send session token: %v", err)
		return nil, fmt.Errorf("relay dialer failed to send session token: %w", err)
	}
	log.Printf("INFO: [Relay Dialer] Sent identification: %s", strings.TrimSpace(identMsg))
	log.Printf("INFO: [Relay Dialer] Handing connection to gRPC for address %s", targetRelayDataAddr)
	return conn, nil
}

func main() {
	clientFlags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	serverAddrActual = clientFlags.String("address", "localhost:32212", "The server address (direct) or relay data address (relay)")
	connectionType = clientFlags.String("connectionType", "direct", "Connection type: 'direct' or 'relay'")
	sessionToken = clientFlags.String("sessionToken", "", "Session token for relay connection")

	err := clientFlags.Parse(os.Args[1:])
	if err != nil {
		log.Printf("FATAL: Error parsing flags: %v", err)
		os.Exit(1) // Exit gracefully after logging
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	a := app.New()
	w := a.NewWindow("Control GRPC client")
	mainWindow = w

	normalSize := fyne.NewSize(1280, 720)
	fullSize := fyne.NewSize(1920, 1080)
	imageCanvas := canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 1920, 1080)))
	imageCanvas.SetMinSize(normalSize)
	imageCanvas.FillMode = canvas.ImageFillStretch

	// Pass the actual server address (from flags) to loadTLSCredentialsFromEmbed
	// so it can be used for ServerName in TLS config.
	tlsCreds, err := loadTLSCredentialsFromEmbed(*serverAddrActual)
	if err != nil {
		// This is a critical setup error, Fatel is appropriate.
		log.Fatalf("FATAL: Cannot load TLS credentials: %v", err)
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(tlsCreds),
		// grpc.WithBlock(), // WithBlock can be useful but also makes DialContext hang until timeout.
		// Context-based timeout in DialContext is generally preferred.
	}

	log.Printf("INFO: Client attempting to connect. Type: '%s', Address: '%s'", *connectionType, *serverAddrActual)

	if *connectionType == "relay" {
		if *sessionToken == "" {
			log.Printf("FATAL: Relay connection type specified but no session token provided.")
			os.Exit(1)
		}
		log.Printf("INFO: Using custom dialer for relay connection to %s with session token %s", *serverAddrActual, *sessionToken)
		// For relay, the ServerName in TLS should ideally be the original target host's ID/name,
		// not the relay's IP. This requires passing more info to client.exe.
		// The current loadTLSCredentialsFromEmbed uses serverAddrActual, which for relay, is the relay's data port.
		// This might lead to TLS errors if the relay isn't the one terminating TLS with the correct cert.
		// Assuming end-to-end TLS for now, where server's cert must match what client expects for original target.
		opts = append(opts, grpc.WithContextDialer(customRelayDialer))
	}

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer dialCancel()

	conn, err := grpc.DialContext(dialCtx, *serverAddrActual, opts...)
	if err != nil {
		log.Printf("ERROR: Could not connect to %s: %v", *serverAddrActual, err)
		// Optionally, show a Fyne dialog here if 'a' (fyne.App) is accessible
		// and then exit. For now, just log and exit.
		if mainWindow != nil {
			dialog.ShowError(fmt.Errorf("Could not connect to server: %v", err), mainWindow)
			// Give a moment for dialog to show before exiting if app isn't running yet.
			// However, if main exits, Fyne app might close too fast.
			// A more robust solution involves running Fyne and gRPC connection in a way
			// that UI can be updated before exiting.
		}
		os.Exit(1) // Exit gracefully after logging
	}
	defer conn.Close()
	log.Printf("INFO: Successfully connected to server/relay at %s", *serverAddrActual)

	remoteControlClient = pb.NewRemoteControlServiceClient(conn)
	filesClient = pb.NewFileTransferServiceClient(conn)

	if filesClient == nil {
		log.Printf("ERROR: Failed to create filesClient!")
		os.Exit(1)
	}

	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()

	stream, err := remoteControlClient.GetFeed(streamCtx)
	if err != nil {
		log.Printf("ERROR: Error creating stream: %v", err)
		if mainWindow != nil {
			dialog.ShowError(fmt.Errorf("Error creating stream: %v", err), mainWindow)
		}
		os.Exit(1) // Exit gracefully
	}

	initRequest := &pb.FeedRequest{
		Message:      "init",
		MouseX:       0,
		MouseY:       0,
		ClientWidth:  1920,
		ClientHeight: 1080,
		Timestamp:    time.Now().UnixNano(),
	}
	if err := stream.Send(initRequest); err != nil {
		log.Printf("ERROR: Error sending initialization message: %v", err)
		if mainWindow != nil {
			dialog.ShowError(fmt.Errorf("Error sending init message: %v", err), mainWindow)
		}
		os.Exit(1) // Exit gracefully
	}

	overlay := newMouseOverlay(stream)
	videoContainer := container.NewStack(imageCanvas, overlay)

	go func() {
		for req := range mouseEvents {
			if err := stream.Send(req); err != nil {
				log.Printf("ERROR: Error sending mouse event: %v", err)
				// If stream is broken, consider cancelling streamCtx or exiting
			}
		}
	}()

	widgetLabel := widget.NewLabel("Video Feed:")
	toggleButtonText := binding.NewString()
	toggleButtonText.Set("Full screen")
	isFull := false
	toggleButton := widget.NewButton("", func() {
		if !isFull {
			w.SetFullScreen(true)
			imageCanvas.SetMinSize(fullSize)
			isFull = true
			toggleButtonText.Set("Exit full screen")
		} else {
			w.SetFullScreen(false)
			imageCanvas.SetMinSize(normalSize)
			isFull = false
			toggleButtonText.Set("Full screen")
		}
		w.Content().Refresh()
	})
	toggleButtonText.AddListener(binding.NewDataListener(func() {
		text, _ := toggleButtonText.Get()
		toggleButton.SetText(text)
	}))
	toggleButton.SetText("Full screen")

	var fileTree *widget.Tree
	fileTree = widget.NewTree(
		createTreeChildrenFunc,
		isTreeNodeBranchFunc,
		createTreeNodeFunc,
		updateTreeNodeFunc,
	)
	fileTree.OnBranchOpened = func(id widget.TreeNodeID) { onTreeBranchOpened(id, fileTree) }
	fileTree.OnBranchClosed = onTreeBranchClosed
	fileTree.OnSelected = onTreeNodeSelected
	treeContainer := container.NewScroll(fileTree)

	getFSButton := widget.NewButton("Files", func() {
		filesWindow := a.NewWindow("File Browser")
		filesWindow.SetContent(treeContainer)
		filesWindow.Resize(fyne.NewSize(500, 600))
		filesWindow.Show()
		fileTree.OpenBranch("")
	})

	pingLabel = widget.NewLabel("RTT: --- ms")
	fpsLabel = widget.NewLabel("FPS: ---") // Initialize FPS label
	topBar := container.NewHBox(widgetLabel, toggleButton, getFSButton, widget.NewSeparator(), pingLabel, widget.NewSeparator(), fpsLabel)
	content := container.NewBorder(topBar, nil, nil, nil, videoContainer)
	w.SetContent(content)

	go startPinger(streamCtx, remoteControlClient)

	ffmpegInputReader, ffmpegInputWriter := io.Pipe()
	ffmpegOutputReader, ffmpegOutputWriter := io.Pipe()

	go func() {
		for parentIdToRefresh := range refreshTreeChan {
			log.Printf("Received refresh signal for children of: '%s'", parentIdToRefresh)
			if fileTree != nil {
				fileTree.Refresh()
			}
		}
	}()

	go runFFmpegProcess(ffmpegInputReader, ffmpegOutputWriter)
	go readFFmpegOutputToBuffer(ffmpegOutputReader, rawFrameBuffer)
	go processRawFramesToImage(rawFrameBuffer, frameImageData)
	go drawFrames(imageCanvas, frameImageData, fpsLabel)
	go forwardVideoFeed(stream, ffmpegInputWriter)

	w.ShowAndRun() // This blocks until the Fyne app is closed.
	// Code after ShowAndRun() will execute when the window is closed.
	log.Println("INFO: Fyne app exited. Client shutting down.")
}

// loadTLSCredentialsFromEmbed loads TLS credentials for the gRPC client from embedded data.
// It now takes the serverAddrString to help determine the ServerName for TLS.
func loadTLSCredentialsFromEmbed(serverAddrString string) (credentials.TransportCredentials, error) {
	clientCert, err := tls.X509KeyPair(clientCertEmbed, clientKeyEmbed)
	if err != nil {
		return nil, fmt.Errorf("failed to load client key pair from embedded data: %w", err)
	}
	serverCertPool := x509.NewCertPool()
	if !serverCertPool.AppendCertsFromPEM(serverCACertEmbed) {
		return nil, fmt.Errorf("failed to append server CA cert: %w", err)
	}

	// Determine ServerName for TLS verification.
	// This should match the CN or a SAN in the server's certificate.
	var tlsServerName string
	if *connectionType == "relay" {
		// IMPORTANT: For relay, ServerName should ideally be the *original target host's ID/name*
		// that the server's certificate is issued for, NOT the relay's IP/address.
		// This requires the client to know the original target ID.
		// For now, if we don't have that original ID easily available here,
		// using the relay's address might work only if the relay itself terminates TLS
		// with a cert valid for its address, which is not the current model.
		// A placeholder or a configurable value might be needed.
		// Using "localhost" as a fallback, but this is likely incorrect for real relay scenarios
		// unless the final server's cert is for "localhost" and it's being tested locally through relay.
		log.Printf("WARN: [TLS] In relay mode, ServerName for TLS is critical. Using 'localhost' as a placeholder. This might need adjustment for production relay setups to use the actual target server's hostname/ID.")
		tlsServerName = "localhost" // This is a placeholder and likely needs to be the actual target HostID/name
	} else {
		// For direct connections, use the host part of the server address.
		host, _, err := net.SplitHostPort(serverAddrString)
		if err != nil {
			log.Printf("WARN: [TLS] Could not parse host from server address '%s' for direct connection: %v. Defaulting ServerName to '%s'.", serverAddrString, err, serverAddrString)
			tlsServerName = serverAddrString // Fallback to full address if SplitHostPort fails (e.g. not host:port)
		} else {
			tlsServerName = host
		}
	}
	log.Printf("INFO: [TLS] Using ServerName: '%s' for TLS configuration.", tlsServerName)

	config := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      serverCertPool,
		MinVersion:   tls.VersionTLS12,
		ServerName:   tlsServerName,
	}
	return credentials.NewTLS(config), nil
}

func startPinger(ctx context.Context, client pb.RemoteControlServiceClient) {
	if client == nil {
		log.Println("Pinger: RemoteControlServiceClient is nil.")
		if pingLabel != nil {
			pingLabel.SetText("RTT: Error (client nil)")
		}
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	log.Println("Pinger started.")
	for {
		select {
		case <-ticker.C:
			startTime := time.Now()
			req := &pb.PingRequest{ClientTimestampNano: startTime.UnixNano()}
			pingCtx, cancelPing := context.WithTimeout(ctx, 1*time.Second)
			resp, err := client.Ping(pingCtx, req)
			cancelPing()
			if err != nil {
				log.Printf("Ping failed: %v", err)
				if pingLabel != nil {
					pingLabel.SetText("RTT: Error")
				}
				if s, ok := status.FromError(err); ok {
					if s.Code() == codes.Unavailable || s.Code() == codes.Canceled {
						log.Println("Pinger: Connection unavailable, stopping.")
						if pingLabel != nil {
							pingLabel.SetText("RTT: N/A (Disconnected)")
						}
						return
					}
				}
				continue
			}
			// Ensure resp is not nil before accessing its fields, though Ping should always return a non-nil response on success.
			if resp == nil {
				log.Printf("WARN: Ping response is nil, client timestamp: %d", req.GetClientTimestampNano())
				continue
			}
			// Example of using the response, though not strictly necessary for RTT calculation here.
			_ = resp.GetClientTimestampNano()

			rttMillis := float64(time.Since(startTime).Nanoseconds()) / 1_000_000.0
			if pingLabel != nil {
				pingLabel.SetText(fmt.Sprintf("RTT: %.2f ms", rttMillis))
			}
		case <-ctx.Done():
			log.Println("Pinger: Context cancelled, stopping.")
			if pingLabel != nil {
				pingLabel.SetText("RTT: N/A")
			}
			return
		}
	}
}

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"flag" // Standard library flag package
	"fmt"
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
	"fyne.io/fyne/v2/dialog"
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
	mainWindow          fyne.Window                       // This is the main application window
	inputEvents         = make(chan *pb.FeedRequest, 120) // Handles mouse & keyboard
	pingLabel           *widget.Label
	fpsLabel            *widget.Label
	remoteControlClient pb.RemoteControlServiceClient

	serverAddrActual *string
	connectionType   *string
	sessionToken     *string
)

// customRelayDialer is used by gRPC when connecting via relay.
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

// sendKeyboardEvent constructs and queues a keyboard event.
func sendKeyboardEvent(eventType, keyName, keyChar string) {
	req := &pb.FeedRequest{
		Message:           "keyboard_event", // Differentiate from mouse_event
		KeyboardEventType: eventType,
		KeyName:           keyName,
		KeyCharStr:        keyChar,
		Timestamp:         time.Now().UnixNano(),
		ClientWidth:       1920, // Or actual dynamic client width if needed
		ClientHeight:      1080, // Or actual dynamic client height
	}

	select {
	case inputEvents <- req:
		// log.Printf("Keyboard event queued: Type: %s, Name: %s, Char: %s", eventType, keyName, keyChar) // Verbose
	default:
		log.Println("Keyboard event dropped (inputEvents channel full)")
	}
}

func main() {
	clientFlags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	serverAddrActual = clientFlags.String("address", "localhost:32212", "The server address (direct) or relay data address (relay)")
	connectionType = clientFlags.String("connectionType", "direct", "Connection type: 'direct' or 'relay'")
	sessionToken = clientFlags.String("sessionToken", "", "Session token for relay connection")

	err := clientFlags.Parse(os.Args[1:])
	if err != nil {
		log.Fatalf("FATAL: Error parsing flags: %v", err)
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	a := app.New()
	w := a.NewWindow("Control GRPC client")
	mainWindow = w // Assign the created window to the global mainWindow variable

	normalSize := fyne.NewSize(1280, 720)
	fullSize := fyne.NewSize(1920, 1080)
	imageCanvas := canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 1920, 1080)))
	imageCanvas.SetMinSize(normalSize)
	imageCanvas.FillMode = canvas.ImageFillStretch

	tlsCreds, err := loadTLSCredentialsFromEmbed(*serverAddrActual)
	if err != nil {
		log.Fatalf("FATAL: Cannot load TLS credentials: %v", err)
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(tlsCreds),
	}

	log.Printf("INFO: Client attempting to connect. Type: '%s', Address: '%s'", *connectionType, *serverAddrActual)

	if *connectionType == "relay" {
		if *sessionToken == "" {
			log.Fatalf("FATAL: Relay connection type specified but no session token provided.")
		}
		log.Printf("INFO: Using custom dialer for relay connection to %s with session token %s", *serverAddrActual, *sessionToken)
		opts = append(opts, grpc.WithContextDialer(customRelayDialer))
	}

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer dialCancel()

	conn, err := grpc.DialContext(dialCtx, *serverAddrActual, opts...)
	if err != nil {
		log.Printf("ERROR: Could not connect to %s: %v", *serverAddrActual, err)
		if a.Driver() != nil && mainWindow != nil {
			go func() {
				time.Sleep(100 * time.Millisecond)
				dialog.ShowError(fmt.Errorf("Could not connect to server: %v", err), mainWindow)
			}()
			time.Sleep(2 * time.Second)
		}
		os.Exit(1)
	}
	defer conn.Close()
	log.Printf("INFO: Successfully connected to server/relay at %s", *serverAddrActual)

	remoteControlClient = pb.NewRemoteControlServiceClient(conn)
	filesClient = pb.NewFileTransferServiceClient(conn)

	if filesClient == nil {
		log.Fatalf("ERROR: Failed to create filesClient!")
	}

	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()

	stream, err := remoteControlClient.GetFeed(streamCtx)
	if err != nil {
		log.Printf("ERROR: Error creating stream: %v", err)
		dialog.ShowError(fmt.Errorf("Error creating stream: %v", err), mainWindow)
		os.Exit(1)
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
		dialog.ShowError(fmt.Errorf("Error sending init message: %v", err), mainWindow)
		os.Exit(1)
	}

	overlay := newMouseOverlay(inputEvents, mainWindow)
	videoContainer := container.NewStack(imageCanvas, overlay)

	go func() {
		for req := range inputEvents {
			if err := stream.Send(req); err != nil {
				log.Printf("ERROR: Error sending input event (type: %s): %v", req.Message, err)
			}
		}
		log.Println("Input event sender goroutine stopped.")
	}()

	// Setup keyboard event handlers on the main window's canvas.
	if mainWindow != nil && mainWindow.Canvas() != nil {
		mainWindow.Canvas().SetOnTypedRune(func(r rune) {
			// log.Printf("Canvas TypedRune: %c", r) // Debug
			sendKeyboardEvent("keychar", "", string(r))
		})
		mainWindow.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
			// log.Printf("Canvas TypedKey: Name=%s", ev.Name) // Debug
			// SetOnTypedKey is typically for a key press (down event).
			// We don't get a distinct "up" event from this handler.
			sendKeyboardEvent("keydown", string(ev.Name), "")
		})
	} else {
		log.Println("Error: mainWindow or its canvas is nil, cannot set keyboard handlers.")
	}

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
		if overlay != nil {
			w.Canvas().Focus(overlay)
		}
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
	fpsLabel = widget.NewLabel("FPS: ---")
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

	if overlay != nil {
		// Request focus on the overlay so it can handle mouse events
		// and also ensure the canvas it sits on is active for keyboard events.
		w.Canvas().Focus(overlay)
	}

	w.ShowAndRun()
	log.Println("INFO: Fyne app exited. Client shutting down.")
	streamCancel()
	close(inputEvents)
}

func loadTLSCredentialsFromEmbed(serverAddrString string) (credentials.TransportCredentials, error) {
	clientCert, err := tls.X509KeyPair(clientCertEmbed, clientKeyEmbed)
	if err != nil {
		return nil, fmt.Errorf("failed to load client key pair from embedded data: %w", err)
	}
	serverCertPool := x509.NewCertPool()
	if !serverCertPool.AppendCertsFromPEM(serverCACertEmbed) {
		return nil, fmt.Errorf("failed to append server CA cert: %w", err)
	}

	var tlsServerName string
	if *connectionType == "relay" {
		log.Printf("WARN: [TLS] In relay mode, ServerName for TLS is critical. Using 'localhost' as a placeholder. This must match the server's certificate subject/SAN for the original host, not the relay.")
		tlsServerName = "localhost"
	} else {
		host, _, err := net.SplitHostPort(serverAddrString)
		if err != nil {
			log.Printf("WARN: [TLS] Could not parse host from server address '%s' for direct connection: %v. Defaulting ServerName to '%s'.", serverAddrString, err, serverAddrString)
			tlsServerName = serverAddrString
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
				s, ok := status.FromError(err)
				if ok && (s.Code() == codes.Unavailable || s.Code() == codes.Canceled) {
					log.Println("Pinger: Connection unavailable, stopping pinger.")
					if pingLabel != nil {
						pingLabel.SetText("RTT: N/A (Disconnected)")
					}
					return
				}
				continue
			}
			if resp == nil {
				log.Printf("WARN: Ping response is nil, client timestamp: %d", req.GetClientTimestampNano())
				continue
			}
			_ = resp.GetClientTimestampNano()

			rttMillis := float64(time.Since(startTime).Nanoseconds()) / 1_000_000.0
			if pingLabel != nil {
				pingLabel.SetText(fmt.Sprintf("RTT: %.2f ms", rttMillis))
			}
		case <-ctx.Done():
			log.Println("Pinger: Main context cancelled, stopping pinger.")
			if pingLabel != nil {
				pingLabel.SetText("RTT: N/A")
			}
			return
		}
	}
}

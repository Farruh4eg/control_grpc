package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"flag" // Standard library flag package
	"fmt"
	"image" // Used by imageCanvas and frameImageData type
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
	// If newMouseOverlay is in handlers.go in a different package, you'd import it:
	// "your_project_module/client/handlers"
	// If file system functions are in a different package:
	// "your_project_module/client/filesystem"
	// If video pipeline functions are in a different package:
	// "your_project_module/client/videopipeline"
)

//go:embed client.crt
var clientCertEmbed []byte

//go:embed client.key
var clientKeyEmbed []byte

//go:embed server.crt
var serverCACertEmbed []byte

var (
	treeDataMutex       sync.RWMutex
	nodesMap            = make(map[string]*pb.FSNode) // Used by file system tree functions
	childrenMap         = make(map[string][]string)   // Used by file system tree functions
	filesClient         pb.FileTransferServiceClient
	refreshTreeChan     = make(chan string, 1)
	mainWindow          fyne.Window                       // This is the main application window
	inputEvents         = make(chan *pb.FeedRequest, 120) // Handles mouse & keyboard
	pingLabel           *widget.Label
	fpsLabel            *widget.Label
	remoteControlClient pb.RemoteControlServiceClient
	terminalClient      pb.TerminalServiceClient // For terminal functionality

	serverAddrActual *string
	connectionType   *string
	sessionToken     *string

	// Terminal window specific variables
	terminalWindow        fyne.Window // To keep track of the terminal window
	currentTerminalStream pb.TerminalService_CommandStreamClient
	terminalStreamCancel  context.CancelFunc // Changed to be specific to the active stream
	terminalOutput        binding.StringList
	terminalInput         *widget.Entry
	terminalScroll        *container.Scroll
	terminalMutex         sync.Mutex // To protect terminal stream resources
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
	mainWindow = w

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
	terminalClient = pb.NewTerminalServiceClient(conn)

	if filesClient == nil {
		log.Fatalf("ERROR: Failed to create filesClient!")
	}
	if terminalClient == nil {
		log.Fatalf("ERROR: Failed to create terminalClient!")
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

	if mainWindow != nil && mainWindow.Canvas() != nil {
		mainWindow.Canvas().SetOnTypedRune(func(r rune) {
			sendKeyboardEvent("keychar", "", string(r))
		})
		mainWindow.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
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

	terminalButton := widget.NewButton("Terminal", func() {
		openTerminalWindow(a)
	})

	pingLabel = widget.NewLabel("RTT: --- ms")
	fpsLabel = widget.NewLabel("FPS: ---")
	topBar := container.NewHBox(widgetLabel, toggleButton, getFSButton, terminalButton, widget.NewSeparator(), pingLabel, widget.NewSeparator(), fpsLabel)
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
		w.Canvas().Focus(overlay)
	}

	w.ShowAndRun()
	log.Println("INFO: Fyne app exited. Client shutting down.")
	streamCancel() // Cancels remote control stream
	close(inputEvents)

	// Clean up terminal stream if it's active
	terminalMutex.Lock()
	if terminalStreamCancel != nil {
		log.Println("DEBUG: main exit - Calling terminalStreamCancel")
		terminalStreamCancel()
		terminalStreamCancel = nil
	}
	terminalMutex.Unlock()
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

// --- Terminal Window Functions (Added) ---

func openTerminalWindow(a fyne.App) {
	terminalMutex.Lock()
	if terminalWindow != nil {
		log.Println("DEBUG: openTerminalWindow - Window already open. Requesting focus.")
		terminalWindow.RequestFocus()
		terminalMutex.Unlock()
		return
	}
	terminalMutex.Unlock() // Unlock early if window not already open

	if terminalClient == nil {
		log.Println("ERROR: openTerminalWindow - Terminal client not initialized.")
		dialog.ShowError(fmt.Errorf("Terminal client not available"), mainWindow)
		return
	}

	w := a.NewWindow("Remote Terminal")

	// Create a new context and cancel function for this specific terminal window instance
	// This is crucial to avoid conflicts if the window is closed and reopened.
	ctx, cancel := context.WithCancel(context.Background())
	log.Printf("DEBUG: openTerminalWindow - Created new stream context %p with cancel func %p", ctx, cancel)

	// Safely update global vars under mutex
	terminalMutex.Lock()
	terminalWindow = w
	terminalStreamCancel = cancel // Store the cancel func for this specific stream
	terminalOutput = binding.NewStringList()
	terminalMutex.Unlock()

	outputList := widget.NewListWithData(terminalOutput,
		func() fyne.CanvasObject {
			return widget.NewLabel("template")
		},
		func(i binding.DataItem, o fyne.CanvasObject) {
			s, _ := i.(binding.String).Get()
			o.(*widget.Label).SetText(s)
		})

	currentScroll := container.NewScroll(outputList) // Use local var for scroll
	currentScroll.SetMinSize(fyne.NewSize(600, 300))

	currentInput := widget.NewEntry() // Use local var for input
	currentInput.SetPlaceHolder("Enter command...")

	sendButton := widget.NewButton("Send", func() {
		sendTerminalCommand(currentInput) // Pass currentInput
	})

	currentInput.OnSubmitted = func(cmd string) {
		sendTerminalCommand(currentInput) // Pass currentInput
	}

	// Update global vars for input and scroll under mutex, though they are primarily used by the current window's closures
	terminalMutex.Lock()
	terminalInput = currentInput
	terminalScroll = currentScroll
	terminalMutex.Unlock()

	inputBox := container.NewBorder(nil, nil, nil, sendButton, currentInput)
	content := container.NewBorder(nil, inputBox, nil, nil, currentScroll)

	w.SetContent(content)
	w.Resize(fyne.NewSize(640, 480))

	log.Println("DEBUG: openTerminalWindow - Attempting to establish terminal command stream...")
	stream, err := terminalClient.CommandStream(ctx) // Use the new context 'ctx'
	if err != nil {
		log.Printf("ERROR: openTerminalWindow - Could not create terminal command stream: %v. Context %p", err, ctx)
		terminalMutex.Lock()
		if terminalOutput != nil {
			terminalOutput.Append(fmt.Sprintf("Error connecting to terminal: %v", err))
		}
		// Clean up context if stream creation failed
		if terminalStreamCancel != nil { // This should be 'cancel' for the local context
			log.Printf("DEBUG: openTerminalWindow - Stream creation failed, calling cancel func %p for context %p", cancel, ctx)
			cancel() // Call the local cancel
		}
		terminalStreamCancel = nil // Nullify global
		currentTerminalStream = nil
		terminalMutex.Unlock()
		w.Show()
		return
	}

	terminalMutex.Lock()
	currentTerminalStream = stream
	terminalMutex.Unlock()

	log.Printf("DEBUG: openTerminalWindow - Terminal command stream established. Stream: %p, Context: %p", stream, ctx)
	terminalMutex.Lock()
	if terminalOutput != nil {
		terminalOutput.Append("Connected to remote terminal.")
	}
	terminalMutex.Unlock()

	go receiveTerminalOutput(stream, ctx) // Pass the specific stream and its context

	w.SetOnClosed(func() {
		log.Printf("DEBUG: SetOnClosed - Terminal window closed by user. Calling cancel func %p for context %p", cancel, ctx)
		cancel() // Call the cancel function associated with this window's stream context

		terminalMutex.Lock()
		if terminalWindow == w { // Ensure we are cleaning up the correct window's resources
			terminalWindow = nil
			currentTerminalStream = nil
			terminalStreamCancel = nil // Clear the global one if it was for this window
			terminalOutput = nil
			terminalInput = nil
			terminalScroll = nil
			log.Println("DEBUG: SetOnClosed - Terminal resources cleaned up.")
		} else {
			log.Println("DEBUG: SetOnClosed - Window closed was not the current terminalWindow, or already cleaned.")
		}
		terminalMutex.Unlock()
	})

	w.Show()
	w.Canvas().Focus(currentInput)
}

// Modified to accept the input widget directly
func sendTerminalCommand(inputWidget *widget.Entry) {
	if inputWidget == nil {
		log.Println("DEBUG: sendTerminalCommand - inputWidget is nil")
		return
	}
	command := inputWidget.Text
	if command == "" {
		return
	}

	terminalMutex.Lock()
	activeStream := currentTerminalStream
	outputBinding := terminalOutput
	scrollArea := terminalScroll
	currentWin := terminalWindow // For focusing input
	terminalMutex.Unlock()

	if activeStream == nil {
		log.Println("DEBUG: sendTerminalCommand - currentTerminalStream is nil. Command: '%s'", command)
		if outputBinding != nil {
			outputBinding.Append("Error: Not connected to terminal.")
			if scrollArea != nil {
				scrollArea.ScrollToBottom()
			}
		}
		return
	}
	log.Printf("DEBUG: sendTerminalCommand - currentTerminalStream: %p. Command: '%s'", activeStream, command)

	if outputBinding != nil {
		displayCmd := fmt.Sprintf("> %s", command)
		outputBinding.Append(displayCmd)
		if scrollArea != nil {
			scrollArea.ScrollToBottom()
		}
	}

	req := &pb.TerminalRequest{Command: command}
	log.Printf("INFO: Sending terminal command: '%s'", command)
	if err := activeStream.Send(req); err != nil {
		log.Printf("ERROR: Failed to send terminal command: %v. Stream: %p", err, activeStream)

		// Clean up the stream as it's likely broken
		terminalMutex.Lock()
		if terminalStreamCancel != nil {
			log.Printf("DEBUG: sendTerminalCommand - Send() error. Calling terminalStreamCancel: %p", terminalStreamCancel)
			terminalStreamCancel()
			terminalStreamCancel = nil
		}
		currentTerminalStream = nil
		terminalMutex.Unlock()

		if outputBinding != nil { // Update UI about the error
			outputBinding.Append(fmt.Sprintf("Error sending command: %v", err))
			if scrollArea != nil {
				scrollArea.ScrollToBottom()
			}
		}
		return
	}

	inputWidget.SetText("") // Clear input field after sending
	if currentWin != nil {  // Check if window still exists
		currentWin.Canvas().Focus(inputWidget) // Re-focus after sending
	}
}

// Modified to accept the specific stream and its context
func receiveTerminalOutput(stream pb.TerminalService_CommandStreamClient, streamCtx context.Context) {
	log.Printf("DEBUG: receiveTerminalOutput - Goroutine started. Stream: %p, Context: %p, Context.Err(): %v", stream, streamCtx, streamCtx.Err())
	defer log.Printf("DEBUG: receiveTerminalOutput - Goroutine stopped. Stream: %p, Context: %p", stream, streamCtx)

	for {
		// Check context before Recv (important if context can be cancelled externally)
		select {
		case <-streamCtx.Done():
			log.Printf("DEBUG: receiveTerminalOutput - Stream context %p was already done before Recv(): %v", streamCtx, streamCtx.Err())
			// Perform cleanup based on the current global state if needed, but this goroutine is for this specific stream.
			terminalMutex.Lock()
			if terminalOutput != nil {
				terminalOutput.Append(fmt.Sprintf("--- Disconnected (context %p done): %v ---", streamCtx, streamCtx.Err()))
			}
			if terminalScroll != nil && terminalOutput != nil {
				terminalScroll.ScrollToBottom()
			}
			// If this stream was the active one, nullify it.
			if currentTerminalStream == stream {
				currentTerminalStream = nil
				// The cancel func for this stream (passed as streamCtx's cancel) should have been called by SetOnClosed or send error
			}
			terminalMutex.Unlock()
			return
		default:
			// Context not done, proceed to Recv
		}

		log.Printf("DEBUG: receiveTerminalOutput - About to call Recv() on stream %p. Context %p. Context.Err(): %v", stream, streamCtx, streamCtx.Err())
		resp, err := stream.Recv()
		log.Printf("DEBUG: receiveTerminalOutput - Recv() on stream %p returned. Raw error: <%T %v>. Response: <%v>", stream, err, err, resp)

		terminalMutex.Lock() // Lock for accessing global UI elements like terminalOutput

		if err != nil {
			log.Printf("DEBUG: receiveTerminalOutput - Recv() error on stream %p. Error details: %v. Stream context %p error before explicit check: %v", stream, err, streamCtx, streamCtx.Err())

			// Determine if the error is due to the context being canceled for this specific stream
			contextErr := streamCtx.Err() // Check our specific context for this stream
			if contextErr == context.Canceled {
				log.Printf("INFO: receiveTerminalOutput - Stream context %p for stream %p was canceled. Error: %v", streamCtx, stream, err)
				if terminalOutput != nil {
					terminalOutput.Append(fmt.Sprintf("--- Terminal disconnected (context %p canceled): %v ---", streamCtx, err))
				}
			} else if err == io.EOF {
				log.Println("INFO: receiveTerminalOutput - Server closed the stream (EOF) for stream %p.", stream)
				if terminalOutput != nil {
					terminalOutput.Append(fmt.Sprintf("--- Terminal session ended by server (stream %p) ---", stream))
				}
			} else { // Other gRPC errors
				log.Printf("ERROR: receiveTerminalOutput - Error receiving from stream %p: %v", stream, err)
				if terminalOutput != nil {
					terminalOutput.Append(fmt.Sprintf("--- Terminal stream error (stream %p): %v ---", stream, err))
				}
			}

			if terminalScroll != nil && terminalOutput != nil {
				terminalScroll.ScrollToBottom()
			}

			// If this specific stream is the one currently active globally, clean it up.
			// The cancel for streamCtx should be handled by SetOnClosed or send error path.
			if currentTerminalStream == stream {
				currentTerminalStream = nil
				// terminalStreamCancel for this stream was 'cancel' passed to openTerminalWindow,
				// which is called by SetOnClosed or send error path.
				// No need to call the global terminalStreamCancel here directly unless it's a fallback.
				log.Printf("DEBUG: receiveTerminalOutput - Setting global currentTerminalStream to nil as stream %p errored.", stream)
			}
			terminalMutex.Unlock()
			return // Exit goroutine for this stream
		}

		// If err is nil, process response
		if terminalOutput == nil {
			log.Println("DEBUG: receiveTerminalOutput - terminalOutput is nil (window likely closed), but received data for stream %p. Discarding.", stream)
			terminalMutex.Unlock()
			// Continue to try and drain the stream if window is closed but stream is not? Or just return?
			// For now, let's assume if terminalOutput is nil, the window is gone and we should stop.
			return
		}

		var prefix string
		switch resp.GetOutputType() {
		case pb.TerminalResponse_STDOUT:
			prefix = ""
		case pb.TerminalResponse_STDERR:
			prefix = "ERR: "
		case pb.TerminalResponse_SYSTEM_MESSAGE:
			prefix = "SYS: "
		case pb.TerminalResponse_ERROR_MESSAGE:
			prefix = "SRV_ERR: "
		default:
			prefix = "UNK: "
		}
		formattedLine := prefix + resp.GetOutputLine()
		terminalOutput.Append(formattedLine)

		if terminalScroll != nil {
			terminalScroll.ScrollToBottom()
		}
		terminalMutex.Unlock()
	}
}

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
	// Add imports for your actual file_system and video_pipeline packages if needed here
	// e.g., "your_project_path/client/file_system"
	// e.g., "your_project_path/client/video_pipeline"
)

//go:embed client.crt
var clientCertEmbed []byte

//go:embed client.key
var clientKeyEmbed []byte

//go:embed server.crt
var serverCACertEmbed []byte

var (
	// treeDataMutex and related maps (nodesMap, childrenMap) would typically move
	// to your file_system package if the tree functions are there.
	// For now, they are left here, but you might need to adjust based on your package structure.
	treeDataMutex sync.RWMutex
	nodesMap      = make(map[string]*pb.FSNode)
	childrenMap   = make(map[string][]string)

	filesClient         pb.FileTransferServiceClient
	refreshTreeChan     = make(chan string, 1)
	mainWindow          fyne.Window                       // This is the main application window
	inputEvents         = make(chan *pb.FeedRequest, 120) // Handles mouse & keyboard
	pingLabel           *widget.Label
	fpsLabel            *widget.Label
	remoteControlClient pb.RemoteControlServiceClient
	terminalClient      pb.TerminalServiceClient // Added for terminal functionality

	serverAddrActual *string
	connectionType   *string
	sessionToken     *string

	// Terminal window specific variables
	terminalWindow        fyne.Window // To keep track of the terminal window
	currentTerminalStream pb.TerminalService_CommandStreamClient
	terminalStreamCancel  context.CancelFunc
	terminalOutput        binding.StringList
	terminalInput         *widget.Entry
	terminalScroll        *container.Scroll
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
		Message:           "keyboard_event",
		KeyboardEventType: eventType,
		KeyName:           keyName,
		KeyCharStr:        keyChar,
		Timestamp:         time.Now().UnixNano(),
		ClientWidth:       1920,
		ClientHeight:      1080,
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
		log.Printf("ERROR: Error creating remote control stream: %v", err)
		dialog.ShowError(fmt.Errorf("Error creating remote control stream: %v", err), mainWindow)
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

	overlay := newMouseOverlay(inputEvents, mainWindow) // Assuming newMouseOverlay is defined elsewhere or in a package
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
	// The following functions (createTreeChildrenFunc, isTreeNodeBranchFunc, etc.)
	// are expected to be available from your client/file_system.go package.
	// You might need to prefix them with the package name, e.g., file_system.CreateTreeChildrenFunc
	// or ensure they are correctly imported and accessible.
	// This example assumes they are globally accessible or you will adjust the calls.
	// fileTree = widget.NewTree(
	// 	file_system.CreateTreeChildrenFunc, // Example: if in file_system package
	// 	file_system.IsTreeNodeBranchFunc,
	// 	file_system.CreateTreeNodeFunc,
	// 	file_system.UpdateTreeNodeFunc,
	// )
	// fileTree.OnBranchOpened = func(id widget.TreeNodeID) { file_system.OnTreeBranchOpened(id, fileTree, filesClient, &treeDataMutex, nodesMap, childrenMap) } // Pass necessary dependencies
	// fileTree.OnBranchClosed = file_system.OnTreeBranchClosed
	// fileTree.OnSelected = file_system.OnTreeNodeSelected

	// For now, as the exact structure of your file_system package is unknown,
	// I'll comment out the tree initialization. You'll need to integrate this
	// with your actual file_system package.
	log.Println("INFO: File tree functionality will need to be integrated from client/file_system.go")

	treeContainer := container.NewScroll(fileTree) // fileTree will be nil here if not initialized above

	getFSButton := widget.NewButton("Files", func() {
		if fileTree == nil {
			dialog.ShowInformation("File Browser", "File tree not initialized.", mainWindow)
			return
		}
		filesWindow := a.NewWindow("File Browser")
		filesWindow.SetContent(treeContainer)
		filesWindow.Resize(fyne.NewSize(500, 600))
		filesWindow.Show()
		if filesClient != nil {
			// This would typically call a function from your file_system package
			// e.g., file_system.OpenRootBranch(fileTree)
			// fileTree.OpenBranch("") // This might need adjustment
		}
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

	// Video pipeline calls are expected to be handled by your client/video_pipeline package
	// For example:
	// videoPipelineCtx, videoPipelineCancel := context.WithCancel(streamCtx) // Or context.Background()
	// defer videoPipelineCancel()
	// go video_pipeline.StartFFmpegAndProcessing(videoPipelineCtx, stream, imageCanvas, fpsLabel)
	log.Println("INFO: Video pipeline functionality will need to be integrated from client/video_pipeline.go")

	go func() {
		for parentIdToRefresh := range refreshTreeChan {
			log.Printf("Received refresh signal for children of: '%s'", parentIdToRefresh)
			if fileTree != nil {
				fileTree.Refresh()
			}
		}
	}()

	if overlay != nil {
		w.Canvas().Focus(overlay)
	}

	w.ShowAndRun()
	log.Println("INFO: Fyne app exited. Client shutting down.")
	streamCancel()
	close(inputEvents)
	if terminalStreamCancel != nil {
		terminalStreamCancel()
	}
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
				if ok && (s.Code() == codes.Unavailable || s.Code() == codes.Canceled || s.Code() == codes.DeadlineExceeded) {
					log.Println("Pinger: Connection unavailable or ping timed out, stopping pinger.")
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

// --- Terminal Window Functions ---

func openTerminalWindow(a fyne.App) {
	if terminalWindow != nil {
		log.Println("Terminal window already open. Requesting focus.")
		terminalWindow.RequestFocus()
		return
	}

	if terminalClient == nil {
		log.Println("ERROR: Terminal client not initialized.")
		dialog.ShowError(fmt.Errorf("Terminal client not available"), mainWindow)
		return
	}

	w := a.NewWindow("Remote Terminal")
	terminalWindow = w

	terminalOutput = binding.NewStringList()

	outputList := widget.NewListWithData(terminalOutput,
		func() fyne.CanvasObject {
			return widget.NewLabel("template")
		},
		func(i binding.DataItem, o fyne.CanvasObject) {
			s, _ := i.(binding.String).Get()
			o.(*widget.Label).SetText(s)
		})

	terminalScroll = container.NewScroll(outputList)
	terminalScroll.SetMinSize(fyne.NewSize(600, 300))

	terminalInput = widget.NewEntry()
	terminalInput.SetPlaceHolder("Enter command...")

	sendButton := widget.NewButton("Send", func() {
		sendTerminalCommand(terminalInput.Text)
	})

	terminalInput.OnSubmitted = func(cmd string) {
		sendTerminalCommand(cmd)
	}

	inputBox := container.NewBorder(nil, nil, nil, sendButton, terminalInput)
	content := container.NewBorder(nil, inputBox, nil, nil, terminalScroll)

	w.SetContent(content)
	w.Resize(fyne.NewSize(640, 480))

	var streamCtx context.Context
	streamCtx, terminalStreamCancel = context.WithCancel(context.Background())

	log.Println("Attempting to establish terminal command stream...")
	stream, err := terminalClient.CommandStream(streamCtx)
	if err != nil {
		log.Printf("ERROR: Could not create terminal command stream: %v", err)
		terminalOutput.Append(fmt.Sprintf("Error connecting to terminal: %v", err))
		currentTerminalStream = nil
		w.Show()
		return
	}
	currentTerminalStream = stream
	log.Println("Terminal command stream established.")
	terminalOutput.Append("Connected to remote terminal.")

	go receiveTerminalOutput()

	w.SetOnClosed(func() {
		log.Println("Terminal window closed by user.")
		if terminalStreamCancel != nil {
			terminalStreamCancel()
			terminalStreamCancel = nil
		}
		currentTerminalStream = nil
		terminalWindow = nil
		terminalOutput = nil
		terminalInput = nil
		terminalScroll = nil
	})

	w.Show()
	w.Canvas().Focus(terminalInput)
}

func sendTerminalCommand(command string) {
	if command == "" {
		return
	}

	if currentTerminalStream == nil {
		log.Println("ERROR: No active terminal stream to send command.")
		if terminalOutput != nil {
			terminalOutput.Append("Error: Not connected to terminal.")
		}
		return
	}

	if terminalOutput != nil {
		displayCmd := fmt.Sprintf("> %s", command)
		terminalOutput.Append(displayCmd)
		if terminalScroll != nil {
			terminalScroll.ScrollToBottom()
		}
	}

	req := &pb.TerminalRequest{Command: command}
	log.Printf("Sending terminal command: '%s'", command)
	if err := currentTerminalStream.Send(req); err != nil {
		log.Printf("ERROR: Failed to send terminal command: %v", err)
		if terminalOutput != nil {
			terminalOutput.Append(fmt.Sprintf("Error sending command: %v", err))
			if terminalScroll != nil {
				terminalScroll.ScrollToBottom()
			}
		}
		if terminalStreamCancel != nil {
			terminalStreamCancel()
		}
		currentTerminalStream = nil
		return
	}

	if terminalInput != nil {
		terminalInput.SetText("")
		if terminalWindow != nil {
			terminalWindow.Canvas().Focus(terminalInput)
		}
	}
}

func receiveTerminalOutput() {
	log.Println("Terminal output receiver goroutine started.")
	defer log.Println("Terminal output receiver goroutine stopped.")

	for {
		if currentTerminalStream == nil {
			log.Println("Terminal output receiver: stream is nil, exiting.")
			if terminalOutput != nil {
				terminalOutput.Append("--- Terminal Disconnected ---")
				if terminalScroll != nil {
					terminalScroll.ScrollToBottom()
				}
			}
			return
		}

		resp, err := currentTerminalStream.Recv()
		if err != nil {
			if terminalStreamCancel != nil {
				select {
				case <-currentTerminalStream.Context().Done():
					log.Printf("Terminal output receiver: Stream context done: %v", currentTerminalStream.Context().Err())
					if terminalOutput != nil {
						terminalOutput.Append(fmt.Sprintf("--- Disconnected from terminal: %v ---", currentTerminalStream.Context().Err()))
					}
					return
				default:
				}
			}

			if err == io.EOF {
				log.Println("Terminal output receiver: Server closed the stream (EOF).")
				if terminalOutput != nil {
					terminalOutput.Append("--- Terminal session ended by server ---")
				}
			} else {
				s, ok := status.FromError(err)
				if ok && (s.Code() == codes.Canceled || s.Code() == codes.Unavailable) {
					log.Printf("Terminal output receiver: Stream canceled or unavailable: %v", err)
					if terminalOutput != nil {
						terminalOutput.Append(fmt.Sprintf("--- Terminal disconnected: %s ---", s.Message()))
					}
				} else {
					log.Printf("Terminal output receiver: Error receiving from stream: %v", err)
					if terminalOutput != nil {
						terminalOutput.Append(fmt.Sprintf("--- Terminal stream error: %v ---", err))
					}
				}
			}
			if terminalScroll != nil && terminalOutput != nil {
				terminalScroll.ScrollToBottom()
			}
			if terminalStreamCancel != nil {
				terminalStreamCancel()
				terminalStreamCancel = nil
			}
			currentTerminalStream = nil
			return
		}

		if terminalOutput == nil {
			log.Println("Terminal output receiver: terminalOutput is nil, likely window closed. Exiting.")
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
	}
}

// Assuming newMouseOverlay is defined in another file or package
// If not, you'll need to provide its definition or remove its usage.
// For example:
// func newMouseOverlay(eventChan chan *pb.FeedRequest, w fyne.Window) fyne.CanvasObject {
// 	log.Println("Placeholder newMouseOverlay called")
// 	return canvas.NewRectangle(color.Transparent) // Return a transparent object
// }

// Add theme import if not already present globally or in a relevant scope
// For icons like FolderIcon, FileIcon
// import "fyne.io/fyne/v2/theme" // Already present in previous version, ensure it's used if tree icons are re-enabled

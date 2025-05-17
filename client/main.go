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
	"fyne.io/fyne/v2/data/binding" // Kept for toggleButtonTextBinding
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	pb "control_grpc/gen/proto"

	// Import for the fynex-widgets EntryEx (assuming package name is 'wx')
	"github.com/matwachich/fynex-widgets"
)

//go:embed client.crt
var clientCertEmbed []byte

//go:embed client.key
var clientKeyEmbed []byte

//go:embed server.crt
var serverCACertEmbed []byte

var (
	inputEvents         = make(chan *pb.FeedRequest, 120)
	pingLabel           *widget.Label
	fpsLabel            *widget.Label
	remoteControlClient pb.RemoteControlServiceClient
	terminalClient      pb.TerminalServiceClient

	terminalWindow        fyne.Window
	currentTerminalStream pb.TerminalService_CommandStreamClient
	terminalStreamCancel  context.CancelFunc
	terminalOutputDisplay *wx.EntryEx // Using EntryEx from fynex-widgets/wx
	terminalInput         *widget.Entry
	terminalScroll        *container.Scroll
	terminalMutex         sync.Mutex

	serverAddrActual *string
	connectionType   *string
	sessionToken     *string
)

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

	currentFyneApp := app.NewWithID("com.example.controlgrpcclient.v5") // Use your unique ID
	mainAppWindow := currentFyneApp.NewWindow("Control GRPC client")

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
		if currentFyneApp.Driver() != nil && mainAppWindow != nil {
			go func() {
				time.Sleep(100 * time.Millisecond)
				dialog.ShowError(fmt.Errorf("Could not connect to server: %v", err), mainAppWindow)
			}()
			time.Sleep(2 * time.Second)
		}
		os.Exit(1)
	}
	defer conn.Close()
	log.Printf("INFO: Successfully connected to server/relay at %s", *serverAddrActual)

	remoteControlClient = pb.NewRemoteControlServiceClient(conn)
	localFilesClient := pb.NewFileTransferServiceClient(conn)
	terminalClient = pb.NewTerminalServiceClient(conn)

	InitializeSharedGlobals(currentFyneApp, mainAppWindow, localFilesClient)
	log.Println("INFO: Shared globals (AppInstance, mainWindow, filesClient) initialized.")

	if remoteControlClient == nil || localFilesClient == nil || terminalClient == nil {
		log.Fatalf("ERROR: Failed to create one or more gRPC clients!")
	}

	streamCtx, streamCancelMain := context.WithCancel(context.Background())
	defer streamCancelMain()

	stream, err := remoteControlClient.GetFeed(streamCtx)
	if err != nil {
		log.Printf("ERROR: Error creating stream: %v", err)
		dialog.ShowError(fmt.Errorf("Error creating stream: %v", mainAppWindow), mainAppWindow)
		os.Exit(1)
	}

	initRequest := &pb.FeedRequest{
		Message: "init", MouseX: 0, MouseY: 0, ClientWidth: 1920, ClientHeight: 1080, Timestamp: time.Now().UnixNano(),
	}
	if err := stream.Send(initRequest); err != nil {
		log.Printf("ERROR: Error sending initialization message: %v", err)
		dialog.ShowError(fmt.Errorf("Error sending init message: %v", err), mainAppWindow)
		os.Exit(1)
	}

	overlay := newMouseOverlay(inputEvents, mainAppWindow)
	videoContainer := container.NewStack(imageCanvas, overlay)

	go func() {
		for req := range inputEvents {
			if err := stream.Send(req); err != nil {
				log.Printf("ERROR: Error sending input event (type: %s): %v", req.Message, err)
			}
		}
		log.Println("Input event sender goroutine stopped.")
	}()

	if mainAppWindow != nil && mainAppWindow.Canvas() != nil {
		mainAppWindow.Canvas().SetOnTypedRune(func(r rune) { sendKeyboardEvent("keychar", "", string(r)) })
		mainAppWindow.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) { sendKeyboardEvent("keydown", string(ev.Name), "") })
	} else {
		log.Println("Error: mainAppWindow or its canvas is nil, cannot set keyboard handlers.")
	}

	widgetLabel := widget.NewLabel("Video Feed:")
	toggleButtonTextBinding := binding.NewString()
	toggleButtonTextBinding.Set("Full screen")
	isFull := false
	toggleButton := widget.NewButton("", func() {
		if !isFull {
			mainAppWindow.SetFullScreen(true)
			imageCanvas.SetMinSize(fullSize)
			isFull = true
			toggleButtonTextBinding.Set("Exit full screen")
		} else {
			mainAppWindow.SetFullScreen(false)
			imageCanvas.SetMinSize(normalSize)
			isFull = false
			toggleButtonTextBinding.Set("Full screen")
		}
		mainAppWindow.Content().Refresh()
		if overlay != nil {
			mainAppWindow.Canvas().Focus(overlay)
		}
	})
	toggleButtonTextBinding.AddListener(binding.NewDataListener(func() {
		text, _ := toggleButtonTextBinding.Get()
		toggleButton.SetText(text)
	}))
	toggleButton.SetText("Full screen")

	var fileTree *widget.Tree
	fileTree = widget.NewTree(
		createTreeChildrenFunc, isTreeNodeBranchFunc, createTreeNodeFunc, updateTreeNodeFunc,
	)
	fileTree.OnBranchOpened = func(id widget.TreeNodeID) { onTreeBranchOpened(id, fileTree) }
	fileTree.OnBranchClosed = onTreeBranchClosed
	fileTree.OnSelected = onTreeNodeSelected
	treeContainer := container.NewScroll(fileTree)

	getFSButton := widget.NewButton("Files", func() {
		filesWindow := AppInstance.NewWindow("File Browser")
		filesWindow.SetContent(treeContainer)
		filesWindow.Resize(fyne.NewSize(500, 600))
		filesWindow.Show()
		if filesClient != nil {
			go fetchChildren("")
		} else {
			log.Println("Files button: filesClient (shared) is nil, cannot fetch root.")
			dialog.ShowError(fmt.Errorf("File client (shared) not initialized"), filesWindow)
		}
	})

	terminalButton := widget.NewButton("Terminal", func() {
		openTerminalWindow(currentFyneApp)
	})

	pingLabel = widget.NewLabel("RTT: --- ms")
	fpsLabel = widget.NewLabel("FPS: ---")
	topBar := container.NewHBox(widgetLabel, toggleButton, getFSButton, terminalButton, widget.NewSeparator(), pingLabel, widget.NewSeparator(), fpsLabel)
	content := container.NewBorder(topBar, nil, nil, nil, videoContainer)
	mainAppWindow.SetContent(content)

	go startPinger(streamCtx, remoteControlClient)

	grpcToFFmpegReader, grpcToFFmpegWriter := io.Pipe()
	ffmpegToBufferReader, ffmpegToBufferWriter := io.Pipe()

	go func() {
		for parentIdToRefresh := range refreshTreeChan {
			log.Printf("Received refresh signal for children of: '%s'", parentIdToRefresh)
			if fileTree != nil {
				fileTree.Refresh()
			}
		}
		log.Println("Tree refresh goroutine stopped.")
	}()

	go runFFmpegProcess(grpcToFFmpegReader, ffmpegToBufferWriter)
	go readFFmpegOutputToBuffer(ffmpegToBufferReader, rawFrameBuffer)
	go processRawFramesToImage(rawFrameBuffer, frameImageData)
	go drawFrames(imageCanvas, frameImageData, fpsLabel)
	go forwardVideoFeed(stream, grpcToFFmpegWriter)

	if overlay != nil {
		mainAppWindow.Canvas().Focus(overlay)
	}

	mainAppWindow.ShowAndRun()
	log.Println("INFO: Fyne app exited. Client shutting down.")
	streamCancelMain()
	close(inputEvents)
	close(refreshTreeChan)

	terminalMutex.Lock()
	if terminalStreamCancel != nil {
		log.Println("DEBUG: main exit - Calling terminalStreamCancel")
		terminalStreamCancel()
		terminalStreamCancel = nil
	}
	terminalMutex.Unlock()
	log.Println("Client shutdown complete.")
}

func loadTLSCredentialsFromEmbed(serverAddrString string) (credentials.TransportCredentials, error) {
	clientCert, err := tls.X509KeyPair(clientCertEmbed, clientKeyEmbed)
	if err != nil {
		return nil, fmt.Errorf("failed to load client key pair from embedded data: %w", err)
	}
	serverCertPool := x509.NewCertPool()
	if !serverCertPool.AppendCertsFromPEM(serverCACertEmbed) {
		return nil, fmt.Errorf("failed to append server CA cert to pool: %w", err)
	}
	var tlsServerName string
	if connectionType != nil && *connectionType == "relay" {
		log.Printf("WARN: [TLS] In relay mode, ServerName for TLS is critical. Using 'localhost' as a placeholder for relay connections.")
		tlsServerName = "localhost"
	} else {
		host, _, err := net.SplitHostPort(serverAddrString)
		if err != nil {
			log.Printf("WARN: [TLS] Could not parse host from server address '%s': %v. Defaulting ServerName to '%s'.", serverAddrString, err, serverAddrString)
			tlsServerName = serverAddrString
		} else {
			tlsServerName = host
		}
	}
	log.Printf("INFO: [TLS] Using ServerName: '%s' for TLS configuration.", tlsServerName)
	config := &tls.Config{
		Certificates: []tls.Certificate{clientCert}, RootCAs: serverCertPool,
		MinVersion: tls.VersionTLS12, ServerName: tlsServerName,
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
					log.Println("Pinger: Connection unavailable or context done, stopping pinger.")
					if pingLabel != nil {
						pingLabel.SetText("RTT: N/A (Disconnected)")
					}
					return
				}
				continue
			}
			if resp == nil {
				log.Printf("WARN: Ping response is nil despite no error")
				continue
			}
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

func appendToTerminalOutput(textChunk string) {
	terminalMutex.Lock()
	defer terminalMutex.Unlock()

	if terminalOutputDisplay == nil {
		log.Println("DEBUG: appendToTerminalOutput - terminalOutputDisplay is nil, cannot append.")
		return
	}

	currentText := terminalOutputDisplay.Text
	newText := currentText + textChunk
	terminalOutputDisplay.SetText(newText) // SetText should refresh EntryEx as well

	if terminalScroll != nil {
		go func() {
			time.Sleep(20 * time.Millisecond)
			terminalMutex.Lock()
			if terminalScroll != nil {
				terminalScroll.ScrollToBottom()
			}
			terminalMutex.Unlock()
		}()
	}
}

func openTerminalWindow(theApp fyne.App) {
	terminalMutex.Lock()
	if terminalWindow != nil {
		log.Println("DEBUG: openTerminalWindow - Window already open. Requesting focus.")
		terminalWindow.RequestFocus()
		terminalMutex.Unlock()
		return
	}
	terminalMutex.Unlock()

	if terminalClient == nil {
		log.Println("ERROR: openTerminalWindow - Terminal client not initialized.")
		parentWin := mainWindow
		if parentWin != nil {
			dialog.ShowError(fmt.Errorf("Terminal client not available"), parentWin)
		} else {
			log.Println("Cannot show terminal error dialog: global mainWindow is nil.")
			if drv := theApp.Driver(); drv != nil && len(drv.AllWindows()) > 0 {
				dialog.ShowError(fmt.Errorf("Terminal client not available (main window nil)"), drv.AllWindows()[0])
			}
		}
		return
	}

	w := theApp.NewWindow("Remote PTY Terminal")
	ctx, thisWindowCancelFunc := context.WithCancel(context.Background())
	log.Printf("DEBUG: openTerminalWindow - Created new stream context %p with cancel func %p", ctx, thisWindowCancelFunc)

	// Use wx.NewEntryEx for terminal output
	// The constructor NewEntryEx(minRows int) makes it multiline if minRows > 1
	currentOutputDisplay := wx.NewEntryEx(10)          // 10 is an arbitrary number for initial visible rows
	currentOutputDisplay.Wrapping = fyne.TextWrapBreak // Or fyne.TextWrapWord
	currentOutputDisplay.SetReadOnly(true)             // Make it non-editable
	// currentOutputDisplay.TextStyle = fyne.TextStyle{Monospace: true} // Optional: for more terminal-like look

	currentScroll := container.NewScroll(currentOutputDisplay)
	currentScroll.SetMinSize(fyne.NewSize(640, 400))

	currentInput := widget.NewEntry()
	currentInput.SetPlaceHolder("Enter command or input here...")

	terminalMutex.Lock()
	terminalWindow = w
	terminalStreamCancel = thisWindowCancelFunc
	terminalOutputDisplay = currentOutputDisplay // Store the EntryEx
	terminalInput = currentInput
	terminalScroll = currentScroll
	terminalMutex.Unlock()

	sendButton := widget.NewButton("Send", func() { sendTerminalCommand(currentInput) })
	currentInput.OnSubmitted = func(cmdText string) { sendTerminalCommand(currentInput) }
	inputBox := container.NewBorder(nil, nil, nil, sendButton, currentInput)
	content := container.NewBorder(nil, inputBox, nil, nil, currentScroll)
	w.SetContent(content)
	w.Resize(fyne.NewSize(700, 500))

	log.Println("DEBUG: openTerminalWindow - Attempting to establish terminal command stream...")
	stream, err := terminalClient.CommandStream(ctx)
	if err != nil {
		log.Printf("ERROR: openTerminalWindow - Could not create terminal command stream: %v. Context %p", err, ctx)
		appendToTerminalOutput(fmt.Sprintf("--- Error connecting to terminal: %v ---", err))
		log.Printf("DEBUG: openTerminalWindow - Stream creation failed, calling thisWindowCancelFunc (%p) for context %p", thisWindowCancelFunc, ctx)
		thisWindowCancelFunc()

		terminalMutex.Lock()
		if terminalWindow == w {
			log.Println("DEBUG: Stream creation failed for the current global terminal window. Setting global terminalStreamCancel to nil.")
			terminalStreamCancel = nil
		}
		terminalMutex.Unlock()
		w.Show()
		return
	}

	terminalMutex.Lock()
	currentTerminalStream = stream
	terminalMutex.Unlock()
	log.Printf("DEBUG: openTerminalWindow - Terminal command stream established. Stream: %p, Context: %p", stream, ctx)
	go receiveTerminalOutput(stream, ctx, thisWindowCancelFunc)

	w.SetOnClosed(func() {
		log.Printf("DEBUG: SetOnClosed - Terminal window %p closed by user. Calling its cancel func %p for context %p", w, thisWindowCancelFunc, ctx)
		thisWindowCancelFunc()

		terminalMutex.Lock()
		if terminalWindow == w {
			terminalWindow = nil
			currentTerminalStream = nil
			log.Println("DEBUG: SetOnClosed - Cleaning global resources for the closed terminal window.")
			terminalStreamCancel = nil
			terminalOutputDisplay = nil
			terminalInput = nil
			terminalScroll = nil
		} else {
			log.Println("DEBUG: SetOnClosed - Closed window was not the current active terminalWindow, or already cleaned/reassigned.")
		}
		terminalMutex.Unlock()
	})
	w.Show()
	w.Canvas().Focus(currentInput)
}

func sendTerminalCommand(inputWidget *widget.Entry) {
	if inputWidget == nil {
		log.Println("DEBUG: sendTerminalCommand - inputWidget is nil")
		return
	}
	commandText := inputWidget.Text

	terminalMutex.Lock()
	activeStream := currentTerminalStream
	cancelFuncForActiveStream := terminalStreamCancel
	currentWin := terminalWindow
	terminalMutex.Unlock()

	if activeStream == nil {
		log.Printf("DEBUG: No active terminal stream. Cannot send: '%s'", commandText)
		appendToTerminalOutput("--- Error: Not connected. ---")
		return
	}
	log.Printf("DEBUG: Sending to stream %p: '%s'", activeStream, commandText)
	req := &pb.TerminalRequest{Command: commandText}

	if err := activeStream.Send(req); err != nil {
		log.Printf("ERROR: Send to terminal stream %p failed: %v", activeStream, err)
		appendToTerminalOutput(fmt.Sprintf("--- Error sending: %v ---", err))

		if cancelFuncForActiveStream != nil {
			log.Printf("DEBUG: Send error on stream %p. Calling its associated cancel func %p.", activeStream, cancelFuncForActiveStream)
			cancelFuncForActiveStream()
		}

		terminalMutex.Lock()
		if currentTerminalStream == activeStream {
			log.Printf("DEBUG: Send error. Clearing global currentTerminalStream (was %p). Global terminalStreamCancel (was %p) is now nilled as it was called.", currentTerminalStream, terminalStreamCancel)
			currentTerminalStream = nil
			terminalStreamCancel = nil
		} else {
			log.Printf("DEBUG: Send error for stream %p, but global currentTerminalStream is now %p. Global terminalStreamCancel (%p) might be for the new stream.", activeStream, currentTerminalStream, terminalStreamCancel)
		}
		terminalMutex.Unlock()
		return
	}
	inputWidget.SetText("")
	if currentWin != nil && inputWidget != nil {
		currentWin.Canvas().Focus(inputWidget)
	}
}

func receiveTerminalOutput(stream pb.TerminalService_CommandStreamClient, streamCtx context.Context, streamCancel context.CancelFunc) {
	log.Printf("DEBUG: receiveTerminalOutput started. Stream: %p, Context: %p, CancelFunc: %p", stream, streamCtx, streamCancel)
	defer func() {
		log.Printf("DEBUG: receiveTerminalOutput stopped. Stream: %p, Context: %p", stream, streamCtx)
		terminalMutex.Lock()
		if currentTerminalStream == stream {
			log.Printf("DEBUG: receiveTerminalOutput exit. Clearing global currentTerminalStream (%p).", currentTerminalStream)
			currentTerminalStream = nil
			terminalStreamCancel = nil
		}
		terminalMutex.Unlock()
	}()

	for {
		select {
		case <-streamCtx.Done():
			log.Printf("DEBUG: receiveTerminalOutput - Context %p done: %v", streamCtx, streamCtx.Err())
			errMsg := fmt.Sprintf("--- PTY Disconnected (context %p: %v) ---", streamCtx, streamCtx.Err())
			appendToTerminalOutput(errMsg)
			return
		default:
		}
		resp, err := stream.Recv()
		if err != nil {
			log.Printf("DEBUG: Recv() error on stream %p (context %p): %v", stream, streamCtx, err)
			errMsg := ""
			if streamCtx.Err() == context.Canceled {
				errMsg = fmt.Sprintf("--- PTY Disconnected (context %p canceled, Recv err: %v) ---", streamCtx, err)
			} else if err == io.EOF {
				errMsg = fmt.Sprintf("--- PTY Session ended by server (EOF on stream %p) ---", stream)
				if streamCancel != nil {
					log.Printf("DEBUG: receiveTerminalOutput - EOF received, calling this stream's cancel func %p", streamCancel)
					streamCancel()
				}
			} else {
				errMsg = fmt.Sprintf("--- PTY Stream error (stream %p): %v ---", stream, err)
				if streamCancel != nil {
					log.Printf("DEBUG: receiveTerminalOutput - Recv error, calling this stream's cancel func %p", streamCancel)
					streamCancel()
				}
			}
			appendToTerminalOutput(errMsg)
			return
		}
		if resp.GetOutputLine() != "" {
			appendToTerminalOutput(resp.GetOutputLine())
		}
		if resp.GetCommandEnded() {
			log.Printf("DEBUG: Server indicated CommandEnded for stream %p. Output: %q", stream, resp.GetOutputLine())
		}
	}
}

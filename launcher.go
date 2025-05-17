package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

const (
	clientAppName           = "client"
	serverAppName           = "server"
	directConnectionTimeout = 5 * time.Second
	defaultRelayControlAddr = "193.23.218.76:34000" // Default address for the relay server's control port
	effectiveHostIDPrefix   = "EFFECTIVE_HOST_ID:"  // Prefix to look for in server's stdout
)

// getExecutablePath determines the full path to an application executable
// located in the same directory as the launcher.
func getExecutablePath(appName string) (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get current executable path: %w", err)
	}
	dir := filepath.Dir(exePath)
	baseName := appName
	if runtime.GOOS == "windows" {
		baseName += ".exe"
	}
	return filepath.Join(dir, baseName), nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	fyneApp := app.New()
	mainWindow := fyneApp.NewWindow("Application Launcher")
	mainWindow.SetFixedSize(true)

	relayServerEntry := widget.NewEntry()
	relayServerEntry.SetPlaceHolder("Relay Server IP:Port (e.g., 193.23.218.76:34000)")
	relayServerEntry.SetText(defaultRelayControlAddr)

	hostButton := widget.NewButton("Become a Host (Direct & Relay)", func() {
		log.Println("INFO: 'Become a Host' clicked.")
		serverPath, err := getExecutablePath(serverAppName)
		if err != nil {
			log.Printf("ERROR: Could not determine path for server: %v", err)
			dialog.ShowError(fmt.Errorf("Could not find server application '%s': %v", serverAppName, err), mainWindow)
			return
		}
		currentRelayAddr := relayServerEntry.Text
		if currentRelayAddr == "" {
			currentRelayAddr = defaultRelayControlAddr
		}

		// The -hostID flag for server.exe is now more of a suggestion or for non-relay mode.
		// The relay server will assign the definitive ID if relay is used.
		cmd := exec.Command(serverPath, "-relay=true", "-hostID=LauncherHost", "-relayServer="+currentRelayAddr)

		// Capture stdout to get the EFFECTIVE_HOST_ID
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			log.Printf("ERROR: Failed to create stdout pipe for server: %v", err)
			dialog.ShowError(fmt.Errorf("Failed to create stdout pipe: %v", err), mainWindow)
			return
		}
		// Capture stderr for logging
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			log.Printf("ERROR: Failed to create stderr pipe for server: %v", err)
			dialog.ShowError(fmt.Errorf("Failed to create stderr pipe: %v", err), mainWindow)
			return
		}

		err = cmd.Start()
		if err != nil {
			log.Printf("ERROR: Failed to launch server '%s': %v", serverPath, err)
			dialog.ShowError(fmt.Errorf("Failed to launch server: %v", err), mainWindow)
			return
		}
		log.Printf("INFO: Server '%s' launched (PID: %d). Using relay: %s. Waiting for Host ID...", serverPath, cmd.Process.Pid, currentRelayAddr)
		initialDialog := dialog.NewInformation("Host Mode", fmt.Sprintf("Server '%s' launched.\nRelay: %s\nWaiting for Host ID from server...", serverAppName, currentRelayAddr), mainWindow)
		initialDialog.Show()

		// Goroutine to read server's stdout
		go func() {
			scanner := bufio.NewScanner(stdoutPipe)
			for scanner.Scan() {
				line := scanner.Text()
				log.Printf("SERVER_STDOUT: %s", line) // Log all stdout from server
				if strings.HasPrefix(line, effectiveHostIDPrefix) {
					hostID := strings.TrimSpace(strings.TrimPrefix(line, effectiveHostIDPrefix))
					log.Printf("INFO: Captured Effective Host ID from server: %s", hostID)

					// Update UI on the main Fyne thread
					fyneApp.SendNotification(&fyne.Notification{
						Title: "Host ID Ready", Content: fmt.Sprintf("Host ID: %s", hostID),
					})

					// Close the initial "waiting" dialog and show one with the ID and copy button
					initialDialog.Hide() // Or find a way to update it if possible / preferred

					idLabel := widget.NewLabel(fmt.Sprintf("Your Host ID: %s", hostID))
					idLabel.Wrapping = fyne.TextWrapWord
					copyButton := widget.NewButton("Copy ID", func() {
						mainWindow.Clipboard().SetContent(hostID)
						dialog.ShowInformation("Copied", "Host ID copied to clipboard!", mainWindow)
					})
					content := container.NewVBox(
						widget.NewLabel("Server is running and registered."),
						idLabel,
						copyButton,
					)
					dialog.ShowCustom("Host Ready", "Close", content, mainWindow)
					return // Stop scanning once ID is found
				}
			}
			if err := scanner.Err(); err != nil {
				log.Printf("ERROR: Reading server stdout: %v", err)
			}
		}()

		// Goroutine to read server's stderr
		go func() {
			scanner := bufio.NewScanner(stderrPipe)
			for scanner.Scan() {
				log.Printf("SERVER_STDERR: %s", scanner.Text())
			}
			if err := scanner.Err(); err != nil {
				log.Printf("ERROR: Reading server stderr: %v", err)
			}
		}()

		// Goroutine to wait for server process to exit
		go func() {
			errWait := cmd.Wait()
			log.Printf("INFO: Server process (PID: %d) exited. Error (if any): %v", cmd.Process.Pid, errWait)
			// Optionally, notify the user that the server has stopped.
			fyneApp.SendNotification(&fyne.Notification{Title: "Server Stopped", Content: "The host server process has exited."})
			// If the "Host Ready" dialog is modal, it might block this.
			// Consider updating a status label in the main window instead of multiple modal dialogs.
		}()
	})

	connectButton := widget.NewButton("Connect to Remote PC", func() {
		log.Println("INFO: 'Connect to Remote PC' clicked.")
		currentRelayAddr := relayServerEntry.Text
		if currentRelayAddr == "" {
			currentRelayAddr = defaultRelayControlAddr
		}
		promptForAddressAndConnect(mainWindow, fyneApp, currentRelayAddr)
	})

	mainWindow.SetContent(container.NewVBox(
		widget.NewLabel("Choose your role:"),
		container.NewBorder(nil, nil, widget.NewLabel("Relay Server:"), nil, relayServerEntry),
		hostButton,
		connectButton,
	))
	mainWindow.Resize(fyne.NewSize(550, 280))
	mainWindow.CenterOnScreen()
	mainWindow.ShowAndRun()
}

// launchClientApplication attempts to launch the client executable.
func launchClientApplication(clientPath, targetAddress string, isRelayConn bool, sessionToken string, parentWindow fyne.Window) {
	connectionType := "direct"
	if isRelayConn {
		connectionType = "relay"
	}
	log.Printf("INFO: Attempting to launch client for %s (via %s connection)", targetAddress, connectionType)

	args := []string{fmt.Sprintf("-address=%s", targetAddress)}
	if isRelayConn {
		args = append(args, "-connectionType=relay")
		args = append(args, fmt.Sprintf("-sessionToken=%s", sessionToken))
	}

	cmd := exec.Command(clientPath, args...)
	// Pipe client's stdout/stderr to launcher's log for debugging
	clientStdout, _ := cmd.StdoutPipe()
	clientStderr, _ := cmd.StderrPipe()

	err := cmd.Start()
	if err != nil {
		errMsg := fmt.Sprintf("Failed to launch client '%s': %v", clientPath, err)
		log.Printf("ERROR: %s", errMsg)
		dialog.ShowError(fmt.Errorf(errMsg), parentWindow)
		return
	}

	// Log client's stdout
	go func() {
		scanner := bufio.NewScanner(clientStdout)
		for scanner.Scan() {
			log.Printf("CLIENT_STDOUT: %s", scanner.Text())
		}
	}()
	// Log client's stderr
	go func() {
		scanner := bufio.NewScanner(clientStderr)
		for scanner.Scan() {
			log.Printf("CLIENT_STDERR: %s", scanner.Text())
		}
	}()

	successMsg := fmt.Sprintf("Client '%s' launched (PID: %d) targeting %s (via %s).",
		filepath.Base(clientPath), cmd.Process.Pid, targetAddress, connectionType)
	log.Printf("INFO: %s", successMsg)
	dialog.ShowInformation("Client Mode", successMsg, parentWindow)

	go func() {
		errWait := cmd.Wait()
		log.Printf("INFO: Client process (PID: %d) exited. Error (if any): %v", cmd.Process.Pid, errWait)
	}()
}

// connectViaRelay attempts connection through a relay server.
func connectViaRelay(targetHostID string, relayControlAddr string) (connected bool, relayDataAddrForClient string, sessionToken string, err error) {
	log.Printf("INFO: [Relay] Attempting to connect to HostID '%s' via relay server %s", targetHostID, relayControlAddr)

	conn, err := net.DialTimeout("tcp", relayControlAddr, 10*time.Second)
	if err != nil {
		return false, "", "", fmt.Errorf("failed to connect to relay control server %s: %w", relayControlAddr, err)
	}
	defer conn.Close()
	log.Printf("INFO: [Relay] Connected to relay control port %s", relayControlAddr)

	cmdStr := fmt.Sprintf("INITIATE_CLIENT_SESSION %s\n", targetHostID)
	_, err = fmt.Fprint(conn, cmdStr)
	if err != nil {
		return false, "", "", fmt.Errorf("failed to send INITIATE_CLIENT_SESSION to relay: %w", err)
	}
	log.Printf("INFO: [Relay] Sent to relay: %s", strings.TrimSpace(cmdStr))

	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, "", "", fmt.Errorf("failed to read response from relay server: %w", err)
	}
	conn.SetReadDeadline(time.Time{}) // Clear deadline

	response = strings.TrimSpace(response)
	log.Printf("INFO: [Relay] Received from relay: %s", response)
	parts := strings.Fields(response)

	if len(parts) > 0 && parts[0] == "SESSION_READY" {
		if len(parts) < 3 {
			return false, "", "", fmt.Errorf("invalid SESSION_READY response from relay (expected port and token): %s", response)
		}
		dynamicPortStr := parts[1]
		sessionTokenOut := parts[2]

		relayHost, _, err := net.SplitHostPort(relayControlAddr)
		if err != nil {
			return false, "", "", fmt.Errorf("could not parse host from relayControlAddr '%s': %w", relayControlAddr, err)
		}
		finalRelayDataAddr := net.JoinHostPort(relayHost, dynamicPortStr)
		log.Printf("INFO: [Relay] Constructed data address for client: %s", finalRelayDataAddr)
		return true, finalRelayDataAddr, sessionTokenOut, nil

	} else if len(parts) > 0 && parts[0] == "ERROR_HOST_NOT_FOUND" {
		return false, "", "", fmt.Errorf("relay server reported HostID '%s' not found", targetHostID)
	} else {
		return false, "", "", fmt.Errorf("unexpected response from relay: %s", response)
	}
}

// promptForAddressAndConnect handles the UI and logic for connecting to a host.
func promptForAddressAndConnect(parentWindow fyne.Window, a fyne.App, relayServerControlAddr string) {
	inputWindow := a.NewWindow("Connect to Host")
	inputWindow.SetFixedSize(true)
	addressEntry := widget.NewEntry()
	addressEntry.SetPlaceHolder("Host's IP:PORT (direct) or HostID (relay)")

	form := &widget.Form{
		Items: []*widget.FormItem{
			{Text: "Target Address/HostID", Widget: addressEntry},
		},
		OnSubmit: func() {
			userInput := addressEntry.Text
			if userInput == "" {
				dialog.ShowInformation("Input Required", "Please enter the target address or HostID.", inputWindow)
				return
			}
			inputWindow.Close()

			clientPath, err := getExecutablePath(clientAppName)
			if err != nil {
				log.Printf("ERROR: Could not find client application: %v", err)
				dialog.ShowError(fmt.Errorf("Could not find client application '%s': %v", clientAppName, err), parentWindow)
				return
			}

			isPotentiallyDirect := strings.Contains(userInput, ":") && !strings.ContainsAny(userInput, " \t\n") // Basic check for IP:Port
			var directErr error

			if isPotentiallyDirect {
				log.Printf("INFO: Attempting direct connection to %s (timeout: %s)...", userInput, directConnectionTimeout)
				ctxDirect, cancelDirect := context.WithTimeout(context.Background(), directConnectionTimeout)
				defer cancelDirect()

				var d net.Dialer
				connDirect, errDial := d.DialContext(ctxDirect, "tcp", userInput)
				directErr = errDial

				if errDial == nil {
					connDirect.Close()
					log.Printf("INFO: Direct TCP connection to %s successful.", userInput)
					launchClientApplication(clientPath, userInput, false, "", parentWindow)
					return
				}
				if ctxDirect.Err() == context.DeadlineExceeded {
					log.Printf("WARN: Direct connection to %s timed out.", userInput)
					directErr = fmt.Errorf("direct connection timed out") // More user-friendly error
				} else {
					log.Printf("WARN: Direct connection to %s failed: %v", userInput, errDial)
				}
			} else {
				log.Printf("INFO: Input '%s' does not look like IP:PORT, skipping direct attempt, proceeding to relay.", userInput)
				directErr = fmt.Errorf("input not in IP:Port format for direct connection")
			}

			// If direct connection failed or was skipped, try relay
			targetHostID := userInput // Assume userInput is HostID for relay
			log.Printf("INFO: Direct connection failed or skipped. Attempting connection via relay for HostID '%s' using relay server %s...", targetHostID, relayServerControlAddr)

			relayConnected, relayedAddressForClient, sessionToken, errRelay := connectViaRelay(targetHostID, relayServerControlAddr)

			if relayConnected {
				log.Printf("INFO: Connection via relay for HostID '%s' successful. Client will connect to %s with token %s.", targetHostID, relayedAddressForClient, sessionToken)
				launchClientApplication(clientPath, relayedAddressForClient, true, sessionToken, parentWindow)
				return
			}

			log.Printf("WARN: Relay connection attempt for HostID '%s' failed: %v", targetHostID, errRelay)
			finalErrMsg := fmt.Sprintf("Failed to connect to target '%s'.\n\nDirect attempt: %v.\nRelay attempt: %v.",
				userInput, directErr, errRelay)
			dialog.ShowError(fmt.Errorf(finalErrMsg), parentWindow)
		},
		OnCancel: func() {
			inputWindow.Close()
		},
	}
	inputWindow.SetContent(form)
	inputWindow.Resize(fyne.NewSize(400, 130))
	inputWindow.CenterOnScreen()
	inputWindow.Show()
}

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
	// Relay connection timeout is handled by connectViaRelay's internal dial and read timeouts
	defaultRelayControlAddr = "localhost:34000" // Default address for the relay server's control port
)

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

	hostButton := widget.NewButton("Become a Host (Direct & Relay)", func() {
		log.Println("INFO: 'Become a Host' clicked.")
		// For simplicity, host ID and relay server can be configured via server.exe flags
		// Or, you could add more UI elements here to configure them.
		serverPath, err := getExecutablePath(serverAppName)
		if err != nil {
			log.Printf("ERROR: Could not determine path for server: %v", err)
			dialog.ShowError(fmt.Errorf("Could not find server application '%s': %v", serverAppName, err), mainWindow)
			return
		}

		// Example: Launch server with relay mode enabled
		// You might want to get hostID and relayServer from user input
		// For now, assume server.exe is configured or uses defaults if these flags are present.
		cmd := exec.Command(serverPath, "-relay=true", "-hostID=MyGamingRig", "-relayServer="+defaultRelayControlAddr)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		err = cmd.Start()
		if err != nil {
			log.Printf("ERROR: Failed to launch server '%s': %v", serverPath, err)
			dialog.ShowError(fmt.Errorf("Failed to launch server: %v", err), mainWindow)
			return
		}
		log.Printf("INFO: Server '%s' launched (PID: %d). Check its window/console for status.", serverPath, cmd.Process.Pid)
		dialog.ShowInformation("Host Mode", fmt.Sprintf("Server application '%s' launched.\nIt will attempt to register with relay if configured.", serverAppName), mainWindow)
	})

	connectButton := widget.NewButton("Connect to Remote PC", func() {
		log.Println("INFO: 'Connect to Remote PC' clicked.")
		promptForAddressAndConnect(mainWindow, fyneApp)
	})

	mainWindow.SetContent(container.NewVBox(
		widget.NewLabel("Choose your role:"),
		hostButton,
		connectButton,
	))
	mainWindow.Resize(fyne.NewSize(550, 250))
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
		args = append(args, "-connectionType=relay")                       // New flag for client
		args = append(args, fmt.Sprintf("-sessionToken=%s", sessionToken)) // New flag
		// targetAddress in relay mode is the relay's data address
	}

	cmd := exec.Command(clientPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Start()
	if err != nil {
		errMsg := fmt.Sprintf("Failed to launch client '%s': %v", clientPath, err)
		log.Printf("ERROR: %s", errMsg)
		dialog.ShowError(fmt.Errorf(errMsg), parentWindow)
		return
	}

	successMsg := fmt.Sprintf("Client '%s' launched (PID: %d) targeting %s (via %s).",
		filepath.Base(clientPath), cmd.Process.Pid, targetAddress, connectionType)
	log.Printf("INFO: %s", successMsg)
	dialog.ShowInformation("Client Mode", successMsg, parentWindow)
}

// connectViaRelay attempts connection through a relay server.
// targetInput is what the user typed (could be hostID).
// relayControlAddr is the address of the relay server's control port.
func connectViaRelay(targetHostID string, relayControlAddr string) (connected bool, relayDataAddr string, sessionToken string, err error) {
	log.Printf("INFO: [Relay] Attempting to connect to HostID '%s' via relay server %s", targetHostID, relayControlAddr)

	conn, err := net.DialTimeout("tcp", relayControlAddr, 10*time.Second)
	if err != nil {
		return false, "", "", fmt.Errorf("failed to connect to relay control server %s: %w", relayControlAddr, err)
	}
	defer conn.Close()
	log.Printf("INFO: [Relay] Connected to relay control port %s", relayControlAddr)

	// Send INITIATE_CLIENT_SESSION command
	cmdStr := fmt.Sprintf("INITIATE_CLIENT_SESSION %s\n", targetHostID)
	_, err = fmt.Fprint(conn, cmdStr)
	if err != nil {
		return false, "", "", fmt.Errorf("failed to send INITIATE_CLIENT_SESSION to relay: %w", err)
	}
	log.Printf("INFO: [Relay] Sent to relay: %s", strings.TrimSpace(cmdStr))

	// Read response from relay server
	// Set a read deadline for the response
	conn.SetReadDeadline(time.Now().Add(15 * time.Second)) // Timeout for relay server to respond
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
			return false, "", "", fmt.Errorf("invalid SESSION_READY response from relay: %s", response)
		}
		// SESSION_READY <dynamic_relay_addr> <session_token>
		return true, parts[1], parts[2], nil
	} else if len(parts) > 0 && parts[0] == "ERROR_HOST_NOT_FOUND" {
		return false, "", "", fmt.Errorf("relay server reported HostID '%s' not found", targetHostID)
	} else {
		return false, "", "", fmt.Errorf("unexpected response from relay: %s", response)
	}
}

func promptForAddressAndConnect(parentWindow fyne.Window, a fyne.App) {
	inputWindow := a.NewWindow("Connect to Host")
	inputWindow.SetFixedSize(true)
	addressEntry := widget.NewEntry()
	addressEntry.SetPlaceHolder("Host's IP:PORT (direct) or HostID (relay)")
	// addressEntry.SetText("localhost:32212") // For direct testing
	// addressEntry.SetText("MyGamingRig") // For relay testing

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

			// Attempt 1: Direct Connection
			// We assume if it contains a colon, it's an IP:Port for direct attempt.
			// This is a basic heuristic.
			isPotentiallyDirect := strings.Contains(userInput, ":")
			var directErr error

			if isPotentiallyDirect {
				log.Printf("INFO: Attempting direct connection to %s (timeout: %s)...", userInput, directConnectionTimeout)
				ctxDirect, cancelDirect := context.WithTimeout(context.Background(), directConnectionTimeout)
				defer cancelDirect()

				var d net.Dialer
				connDirect, errDial := d.DialContext(ctxDirect, "tcp", userInput)
				directErr = errDial // Store direct error

				if errDial == nil {
					connDirect.Close()
					log.Printf("INFO: Direct TCP connection to %s successful.", userInput)
					launchClientApplication(clientPath, userInput, false, "", parentWindow)
					return
				}
				// Log direct connection failure
				if ctxDirect.Err() == context.DeadlineExceeded {
					log.Printf("WARN: Direct connection to %s timed out.", userInput)
				} else {
					log.Printf("WARN: Direct connection to %s failed: %v", userInput, errDial)
				}
			} else {
				log.Printf("INFO: Input '%s' does not look like IP:PORT, skipping direct attempt, proceeding to relay.", userInput)
				directErr = fmt.Errorf("input format not suitable for direct dial") // Placeholder error
			}

			// Attempt 2: Relay Connection
			// User input is treated as targetHostID for relay
			targetHostID := userInput
			log.Printf("INFO: Direct connection failed or skipped. Attempting connection via relay for HostID '%s'...", targetHostID)

			relayConnected, relayedAddress, sessionToken, errRelay := connectViaRelay(targetHostID, defaultRelayControlAddr)

			if relayConnected {
				log.Printf("INFO: Connection via relay for HostID '%s' successful. Client will connect to %s with token %s.", targetHostID, relayedAddress, sessionToken)
				launchClientApplication(clientPath, relayedAddress, true, sessionToken, parentWindow)
				return
			}

			// Relay connection also failed
			log.Printf("WARN: Relay connection attempt for HostID '%s' failed: %v", targetHostID, errRelay)

			// Both attempts failed
			finalErrMsg := fmt.Sprintf("Failed to connect to target '%s'.\n\nDirect attempt: %v.\nRelay attempt: %v.",
				userInput,
				directErr, // Error from direct attempt
				errRelay)  // Error from relay attempt
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

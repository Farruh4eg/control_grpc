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

	// Allow user to specify relay server address
	relayServerEntry := widget.NewEntry()
	relayServerEntry.SetPlaceHolder("Relay Server IP:Port (e.g., 193.23.218.76:34000)")
	relayServerEntry.SetText(defaultRelayControlAddr) // Default value

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
			currentRelayAddr = defaultRelayControlAddr // Fallback if user clears it
		}

		cmd := exec.Command(serverPath, "-relay=true", "-hostID=MyGamingRig", "-relayServer="+currentRelayAddr)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		err = cmd.Start()
		if err != nil {
			log.Printf("ERROR: Failed to launch server '%s': %v", serverPath, err)
			dialog.ShowError(fmt.Errorf("Failed to launch server: %v", err), mainWindow)
			return
		}
		log.Printf("INFO: Server '%s' launched (PID: %d). Using relay: %s", serverPath, cmd.Process.Pid, currentRelayAddr)
		dialog.ShowInformation("Host Mode", fmt.Sprintf("Server application '%s' launched.\nRelay server: %s.", serverAppName, currentRelayAddr), mainWindow)
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
	mainWindow.Resize(fyne.NewSize(550, 280)) // Adjusted size for new entry
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
// relayControlAddr is the full IP:Port of the relay server's control service.
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
	conn.SetReadDeadline(time.Time{})

	response = strings.TrimSpace(response)
	log.Printf("INFO: [Relay] Received from relay: %s", response)
	parts := strings.Fields(response)

	if len(parts) > 0 && parts[0] == "SESSION_READY" {
		if len(parts) < 3 {
			return false, "", "", fmt.Errorf("invalid SESSION_READY response from relay (expected port and token): %s", response)
		}
		// SESSION_READY <dynamic_port_str> <session_token>
		dynamicPortStr := parts[1]
		sessionTokenOut := parts[2]

		// The launcher already knows the relay's public IP (host part of relayControlAddr).
		// Combine it with the dynamic port received.
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

// promptForAddressAndConnect now takes relayServerControlAddr from the UI
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

			isPotentiallyDirect := strings.Contains(userInput, ":")
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
				} else {
					log.Printf("WARN: Direct connection to %s failed: %v", userInput, errDial)
				}
			} else {
				log.Printf("INFO: Input '%s' does not look like IP:PORT, skipping direct attempt, proceeding to relay.", userInput)
				directErr = fmt.Errorf("input format not suitable for direct dial")
			}

			targetHostID := userInput
			log.Printf("INFO: Direct connection failed or skipped. Attempting connection via relay for HostID '%s' using relay server %s...", targetHostID, relayServerControlAddr)

			// Pass the relayServerControlAddr (from UI) to connectViaRelay
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

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
	"golang.org/x/crypto/bcrypt" // Import bcrypt
)

const (
	clientAppName           = "client"
	serverAppName           = "server"
	directConnectionTimeout = 5 * time.Second
	defaultRelayControlAddr = "193.23.218.76:34000"
	effectiveHostIDPrefix   = "EFFECTIVE_HOST_ID:"
	bcryptCost              = 12 // Cost factor for bcrypt hashing
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

		// Create a password entry widget
		passwordEntryWidget := widget.NewPasswordEntry()
		passwordEntryWidget.SetPlaceHolder("Leave empty for no password")

		// Create a form dialog for password input
		formItems := []*widget.FormItem{
			{Text: "Session Password", Widget: passwordEntryWidget, HintText: "Enter a password for this session."},
		}

		passwordDialog := dialog.NewForm("Set Session Password", "Set", "Cancel", formItems, func(ok bool) {
			if !ok {
				log.Println("INFO: Host cancelled password input.")
				return // User cancelled
			}

			plainPassword := passwordEntryWidget.Text
			hashedPassword := ""

			if plainPassword == "" {
				log.Println("INFO: Host chose not to set a password.")
			} else {
				log.Println("INFO: Host set a password. Hashing it...")
				// Hash the password
				hashBytes, err := bcrypt.GenerateFromPassword([]byte(plainPassword), bcryptCost)
				if err != nil {
					log.Printf("ERROR: Failed to hash password: %v", err)
					dialog.ShowError(fmt.Errorf("Failed to secure password: %v", err), mainWindow)
					return
				}
				hashedPassword = string(hashBytes)
				log.Println("INFO: Password hashed successfully.")
			}
			// Pass the hashed password (or empty string) to the server process
			launchServerProcess(mainWindow, fyneApp, relayServerEntry.Text, hashedPassword)
		}, mainWindow)

		passwordDialog.Show()
	})

	connectButton := widget.NewButton("Connect to Remote PC", func() {
		log.Println("INFO: 'Connect to Remote PC' clicked.")
		currentRelayAddr := relayServerEntry.Text
		if currentRelayAddr == "" {
			currentRelayAddr = defaultRelayControlAddr
		}
		promptForAddressAndPasswordAndConnect(mainWindow, fyneApp, currentRelayAddr)
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

// launchServerProcess now receives hashedPassword (which could be empty)
func launchServerProcess(parentWindow fyne.Window, fyneApp fyne.App, relayAddr, hashedPassword string) {
	serverPath, err := getExecutablePath(serverAppName)
	if err != nil {
		log.Printf("ERROR: Could not determine path for server: %v", err)
		dialog.ShowError(fmt.Errorf("Could not find server application '%s': %v", serverAppName, err), parentWindow)
		return
	}
	currentRelayAddr := relayAddr
	if currentRelayAddr == "" {
		currentRelayAddr = defaultRelayControlAddr
	}

	args := []string{"-relay=true", "-hostID=LauncherHost", "-relayServer=" + currentRelayAddr}
	// Pass the hashed password (or empty string if no password was set)
	if hashedPassword != "" {
		args = append(args, "-sessionPassword="+hashedPassword)
	}

	cmd := exec.Command(serverPath, args...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("ERROR: Failed to create stdout pipe for server: %v", err)
		dialog.ShowError(fmt.Errorf("Failed to create stdout pipe: %v", err), parentWindow)
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("ERROR: Failed to create stderr pipe for server: %v", err)
		dialog.ShowError(fmt.Errorf("Failed to create stderr pipe: %v", err), parentWindow)
		return
	}

	err = cmd.Start()
	if err != nil {
		log.Printf("ERROR: Failed to launch server '%s': %v", serverPath, err)
		dialog.ShowError(fmt.Errorf("Failed to launch server: %v", err), parentWindow)
		return
	}
	log.Printf("INFO: Server '%s' launched (PID: %d). Relay: %s. Password protection: %t. Waiting for Host ID...",
		serverPath, cmd.Process.Pid, currentRelayAddr, hashedPassword != "")

	initialDialog := dialog.NewInformation("Host Mode",
		fmt.Sprintf("Server '%s' launched.\nRelay: %s\nPassword Protected: %t\nWaiting for Host ID...",
			serverAppName, currentRelayAddr, hashedPassword != ""), parentWindow)
	initialDialog.Show()

	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Text()
			log.Printf("SERVER_STDOUT: %s", line)
			if strings.HasPrefix(line, effectiveHostIDPrefix) {
				hostID := strings.TrimSpace(strings.TrimPrefix(line, effectiveHostIDPrefix))
				log.Printf("INFO: Captured Effective Host ID from server: %s", hostID)

				fyneApp.SendNotification(&fyne.Notification{
					Title: "Host ID Ready", Content: fmt.Sprintf("Host ID: %s", hostID),
				})
				initialDialog.Hide()

				idLabel := widget.NewLabel(fmt.Sprintf("Your Host ID: %s", hostID))
				idLabel.Wrapping = fyne.TextWrapWord
				passwordMsg := "Not password protected."
				if hashedPassword != "" { // Check if a hash was passed, indicating password was set
					passwordMsg = "Session is password protected."
				}
				passwordLabel := widget.NewLabel(passwordMsg)
				copyButton := widget.NewButton("Copy ID", func() {
					parentWindow.Clipboard().SetContent(hostID)
					dialog.ShowInformation("Copied", "Host ID copied to clipboard!", parentWindow)
				})
				content := container.NewVBox(
					widget.NewLabel("Server is running and registered."),
					idLabel,
					passwordLabel,
					copyButton,
				)
				dialog.ShowCustom("Host Ready", "Close", content, parentWindow)
				return
			}
		}
		if err := scanner.Err(); err != nil {
			log.Printf("ERROR: Reading server stdout: %v", err)
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			log.Printf("SERVER_STDERR: %s", scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			log.Printf("ERROR: Reading server stderr: %v", err)
		}
	}()

	go func() {
		errWait := cmd.Wait()
		log.Printf("INFO: Server process (PID: %d) exited. Error (if any): %v", cmd.Process.Pid, errWait)
		fyneApp.SendNotification(&fyne.Notification{Title: "Server Stopped", Content: "The host server process has exited."})
	}()
}

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
	clientStdout, _ := cmd.StdoutPipe()
	clientStderr, _ := cmd.StderrPipe()

	err := cmd.Start()
	if err != nil {
		errMsg := fmt.Sprintf("Failed to launch client '%s': %v", clientPath, err)
		log.Printf("ERROR: %s", errMsg)
		dialog.ShowError(fmt.Errorf(errMsg), parentWindow)
		return
	}

	go func() {
		scanner := bufio.NewScanner(clientStdout)
		for scanner.Scan() {
			log.Printf("CLIENT_STDOUT: %s", scanner.Text())
		}
	}()
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

// connectViaRelay sends the PLAIN TEXT password entered by the connecting client.
// The host will compare this against its stored HASH.
func connectViaRelay(targetHostID, plainTextPassword, relayControlAddr string) (connected bool, relayDataAddrForClient string, sessionToken string, err error) {
	log.Printf("INFO: [Relay] Attempting to connect to HostID '%s' via relay server %s (password provided for verification: %t)",
		targetHostID, relayControlAddr, plainTextPassword != "")

	conn, err := net.DialTimeout("tcp", relayControlAddr, 10*time.Second)
	if err != nil {
		return false, "", "", fmt.Errorf("failed to connect to relay control server %s: %w", relayControlAddr, err)
	}
	defer conn.Close()
	log.Printf("INFO: [Relay] Connected to relay control port %s", relayControlAddr)

	var cmdStr string
	if plainTextPassword == "" {
		cmdStr = fmt.Sprintf("INITIATE_CLIENT_SESSION %s\n", targetHostID)
	} else {
		// Send the plain text password for the host to verify against its hash
		cmdStr = fmt.Sprintf("INITIATE_CLIENT_SESSION %s %s\n", targetHostID, plainTextPassword)
	}

	_, err = fmt.Fprint(conn, cmdStr)
	if err != nil {
		return false, "", "", fmt.Errorf("failed to send INITIATE_CLIENT_SESSION to relay: %w", err)
	}
	log.Printf("INFO: [Relay] Sent to relay: %s", strings.TrimSpace(cmdStr))

	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, "", "", fmt.Errorf("failed to read response from relay server: %w", err)
	}
	conn.SetReadDeadline(time.Time{})

	response = strings.TrimSpace(response)
	log.Printf("INFO: [Relay] Received from relay: %s", response)
	parts := strings.Fields(response)

	if len(parts) > 0 {
		switch parts[0] {
		case "SESSION_READY":
			if len(parts) < 3 {
				return false, "", "", fmt.Errorf("invalid SESSION_READY response from relay: %s", response)
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
		case "ERROR_HOST_NOT_FOUND":
			return false, "", "", fmt.Errorf("relay server reported HostID '%s' not found", targetHostID)
		case "ERROR_AUTHENTICATION_FAILED":
			return false, "", "", fmt.Errorf("authentication failed for HostID '%s'", targetHostID)
		default:
			return false, "", "", fmt.Errorf("unexpected response from relay: %s", response)
		}
	}
	return false, "", "", fmt.Errorf("empty or invalid response from relay: %s", response)
}

func promptForAddressAndPasswordAndConnect(parentWindow fyne.Window, a fyne.App, relayServerControlAddr string) {
	inputWindow := a.NewWindow("Connect to Host")
	inputWindow.SetFixedSize(true)

	hostIDEntry := widget.NewEntry()
	hostIDEntry.SetPlaceHolder("Host's IP:PORT (direct) or HostID (relay)")

	passwordEntryWidget := widget.NewPasswordEntry() // Use widget.NewPasswordEntry for the input field
	passwordEntryWidget.SetPlaceHolder("Password (if host requires it)")

	formItems := []*widget.FormItem{
		{Text: "Target Address/HostID", Widget: hostIDEntry},
		{Text: "Password (for Relay)", Widget: passwordEntryWidget}, // Use the widget here
	}

	form := &widget.Form{
		Items: formItems,
		OnSubmit: func() {
			userInput := hostIDEntry.Text
			// This is the plain text password the user enters to connect
			plainTextPasswordAttempt := passwordEntryWidget.Text
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

			isPotentiallyDirect := strings.Contains(userInput, ":") && !strings.ContainsAny(userInput, " \t\n")
			var directErr error

			if isPotentiallyDirect {
				log.Printf("INFO: Attempting direct connection to %s ...", userInput)
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
					directErr = fmt.Errorf("direct connection timed out")
				} else {
					log.Printf("WARN: Direct connection to %s failed: %v", userInput, errDial)
				}
			} else {
				log.Printf("INFO: Input '%s' does not look like IP:PORT, proceeding to relay.", userInput)
				directErr = fmt.Errorf("input not IP:Port format")
			}

			targetHostID := userInput
			log.Printf("INFO: Direct connection failed/skipped. Attempting relay for HostID '%s' using relay %s...", targetHostID, relayServerControlAddr)

			// Pass the plainTextPasswordAttempt to connectViaRelay
			relayConnected, relayedAddressForClient, sessionToken, errRelay := connectViaRelay(targetHostID, plainTextPasswordAttempt, relayServerControlAddr)

			if relayConnected {
				log.Printf("INFO: Connection via relay for HostID '%s' successful. Client to connect to %s.", targetHostID, relayedAddressForClient)
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
	inputWindow.Resize(fyne.NewSize(400, 180))
	inputWindow.CenterOnScreen()
	inputWindow.Show()
}

package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid" // For session tokens
)

const (
	controlPort     = "34000"          // Main port for control messages from hosts and launchers
	dataConnTimeout = 15 * time.Second // Timeout for client/host proxy to connect to data port
	identTimeout    = 5 * time.Second  // Timeout for an incoming data connection to identify itself
)

// RelayServer manages the state of the relay.
type RelayServer struct {
	hostControlConns map[string]net.Conn // hostID -> control connection from host's sidecar
	mu               sync.Mutex
}

// NewRelayServer creates a new relay server instance.
func NewRelayServer() *RelayServer {
	return &RelayServer{
		hostControlConns: make(map[string]net.Conn),
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile) // Include file and line number in logs
	relay := NewRelayServer()
	listener, err := net.Listen("tcp", ":"+controlPort)
	if err != nil {
		log.Fatalf("FATAL: Failed to listen on control port %s: %v", controlPort, err)
	}
	defer listener.Close()
	log.Printf("INFO: Relay server listening for control connections on port %s", controlPort)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("ERROR: Failed to accept control connection: %v", err)
			continue
		}
		go relay.handleControlConnection(conn)
	}
}

// handleControlConnection processes messages from a control connection (either host sidecar or launcher).
func (r *RelayServer) handleControlConnection(conn net.Conn) {
	defer conn.Close()
	remoteAddr := conn.RemoteAddr().String()
	log.Printf("INFO: New control connection from: %s", remoteAddr)
	reader := bufio.NewReader(conn)

	var registeredHostID string

	for {
		message, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				log.Printf("ERROR: Reading from control connection %s: %v", remoteAddr, err)
			} else {
				log.Printf("INFO: Control connection %s closed (EOF)", remoteAddr)
			}
			if registeredHostID != "" {
				r.mu.Lock()
				if r.hostControlConns[registeredHostID] == conn {
					log.Printf("INFO: Host '%s' (control conn %s) disconnected. Removing from registry.", registeredHostID, remoteAddr)
					delete(r.hostControlConns, registeredHostID)
				}
				r.mu.Unlock()
			}
			return
		}

		message = strings.TrimSpace(message)
		parts := strings.Fields(message)
		if len(parts) == 0 {
			continue
		}

		command := parts[0]
		log.Printf("DEBUG: Control command from %s: %s, Args: %v", remoteAddr, command, parts[1:])

		switch command {
		case "REGISTER_HOST":
			if len(parts) < 2 {
				fmt.Fprintln(conn, "ERROR Invalid REGISTER_HOST. Usage: REGISTER_HOST <host_id>")
				continue
			}
			hostID := parts[1]
			r.mu.Lock()
			if oldConn, exists := r.hostControlConns[hostID]; exists {
				log.Printf("WARN: Host ID '%s' re-registering. Old control conn: %s, New: %s", hostID, oldConn.RemoteAddr(), remoteAddr)
				// oldConn.Close() // Optionally close the old one
			}
			r.hostControlConns[hostID] = conn
			registeredHostID = hostID
			r.mu.Unlock()
			log.Printf("INFO: Host '%s' registered with control connection %s", hostID, remoteAddr)
			fmt.Fprintf(conn, "HOST_REGISTERED %s\n", hostID)

		case "INITIATE_CLIENT_SESSION":
			if len(parts) < 2 {
				fmt.Fprintln(conn, "ERROR Invalid INITIATE_CLIENT_SESSION. Usage: INITIATE_CLIENT_SESSION <target_host_id>")
				continue
			}
			targetHostID := parts[1]

			r.mu.Lock()
			hostControlConn, hostIsRegistered := r.hostControlConns[targetHostID]
			r.mu.Unlock()

			if !hostIsRegistered {
				log.Printf("WARN: Target host '%s' not found for client session from %s", targetHostID, remoteAddr)
				fmt.Fprintf(conn, "ERROR_HOST_NOT_FOUND %s\n", targetHostID)
				continue
			}

			sessionToken := uuid.New().String()
			dataListener, err := net.Listen("tcp", ":0") // OS chooses a free port
			if err != nil {
				log.Printf("ERROR: Failed to create dynamic data listener for session %s: %v", sessionToken, err)
				fmt.Fprintf(conn, "ERROR_RELAY_INTERNAL Failed to create data port\n")
				continue
			}

			// Extract the port from the listener's address
			tcpAddr, ok := dataListener.Addr().(*net.TCPAddr)
			if !ok {
				log.Printf("ERROR: Session %s: Could not get TCP address from data listener.", sessionToken)
				fmt.Fprintf(conn, "ERROR_RELAY_INTERNAL Failed to get data port details\n")
				dataListener.Close()
				continue
			}
			dynamicPort := tcpAddr.Port
			log.Printf("INFO: Session %s for host '%s': Dynamic data listener on port %d (will be combined with relay's public IP by launcher)", sessionToken, targetHostID, dynamicPort)

			// Tell client (launcher) the DYNAMIC PORT and session token.
			// The launcher will combine this port with the relay's public IP it already knows.
			fmt.Fprintf(conn, "SESSION_READY %d %s\n", dynamicPort, sessionToken) // Send only port
			log.Printf("INFO: Session %s: Notified launcher %s to have client.exe connect to relay's public IP on port %d", sessionToken, remoteAddr, dynamicPort)

			// Tell target host (via its control conn) to connect its proxy to the relay's public IP and dynamic data port
			// The host will also need to know the relay's public IP. For now, it uses relayServerAddr flag.
			// The CREATE_TUNNEL command will now send the port. The host proxy will combine it with its configured relayServerAddr's host part.
			if hostControlConn != nil {
				// The relay server's public IP is what the host used in its -relayServer flag.
				// It will combine the host part of that with this new port.
				// So, we only need to send the port to the host as well.
				_, errSend := fmt.Fprintf(hostControlConn, "CREATE_TUNNEL %d %s\n", dynamicPort, sessionToken)
				if errSend != nil {
					log.Printf("ERROR: Session %s: Failed to send CREATE_TUNNEL (port %d) to host '%s' (%s): %v. Aborting session.", sessionToken, dynamicPort, targetHostID, hostControlConn.RemoteAddr(), errSend)
					fmt.Fprintf(conn, "ERROR_RELAY_INTERNAL Failed to notify host.\n")
					dataListener.Close()
					r.mu.Lock()
					if r.hostControlConns[targetHostID] == hostControlConn {
						delete(r.hostControlConns, targetHostID)
						log.Printf("INFO: Removed host '%s' due to control connection error.", targetHostID)
					}
					r.mu.Unlock()
					continue
				}
				log.Printf("INFO: Session %s: Notified host '%s' (control %s) to create tunnel to relay's public IP on port %d", sessionToken, targetHostID, hostControlConn.RemoteAddr(), dynamicPort)
			} else {
				log.Printf("CRITICAL: Session %s: Host '%s' registered but control connection is nil. Aborting.", sessionToken, targetHostID)
				fmt.Fprintf(conn, "ERROR_RELAY_INTERNAL Host control connection issue.\n")
				dataListener.Close()
				continue
			}

			go r.manageDataSession(dataListener, sessionToken, targetHostID, dynamicPort) // Pass port for logging
		default:
			log.Printf("WARN: Unknown control command from %s: '%s'", remoteAddr, message)
			fmt.Fprintf(conn, "ERROR Unknown command: %s\n", command)
		}
	}
}

// manageDataSession waits for two connections on the dataListener.
func (r *RelayServer) manageDataSession(dataListener net.Listener, sessionToken string, hostID string, port int) {
	defer dataListener.Close()
	log.Printf("INFO: Session %s (Host '%s'): Waiting for data connections on port %d (timeout: %s)", sessionToken, hostID, port, dataConnTimeout*2)

	var clientAppConn, hostProxyConn net.Conn
	var wg sync.WaitGroup
	wg.Add(2)

	connChan := make(chan net.Conn, 2)
	errChan := make(chan error, 2)

	go func() {
		defer wg.Done()
		conn, err := dataListener.Accept()
		if err != nil {
			errChan <- fmt.Errorf("accept1 failed: %w", err)
			return
		}
		connChan <- conn
	}()

	go func() {
		defer wg.Done()
		conn, err := dataListener.Accept()
		if err != nil {
			errChan <- fmt.Errorf("accept2 failed: %w", err)
			return
		}
		connChan <- conn
	}()

	acceptTimeout := time.After(dataConnTimeout * 2)
	var acceptedConns []net.Conn

LoopAccept:
	for i := 0; i < 2; i++ {
		select {
		case conn := <-connChan:
			acceptedConns = append(acceptedConns, conn)
		case err := <-errChan:
			if strings.Contains(err.Error(), "use of closed network connection") {
				log.Printf("INFO: Session %s: Data listener was closed.", sessionToken)
			} else {
				log.Printf("ERROR: Session %s: Error accepting data connection: %v", sessionToken, err)
			}
			break LoopAccept
		case <-acceptTimeout:
			log.Printf("WARN: Session %s: Timed out waiting for all data connections on port %d.", sessionToken, port)
			break LoopAccept
		}
	}
	wg.Wait()

	for _, conn := range acceptedConns {
		conn.SetReadDeadline(time.Now().Add(identTimeout))
		identifier, err := bufio.NewReader(conn).ReadString('\n')
		conn.SetReadDeadline(time.Time{})

		if err != nil {
			log.Printf("WARN: Session %s: Failed to read identification from %s on port %d: %v. Closing it.", sessionToken, conn.RemoteAddr(), port, err)
			conn.Close()
			continue
		}
		identifier = strings.TrimSpace(identifier)
		parts := strings.Fields(identifier)

		if len(parts) != 3 || parts[0] != "SESSION_TOKEN" || parts[1] != sessionToken {
			log.Printf("WARN: Session %s: Invalid identification from %s on port %d: '%s'. Closing it.", sessionToken, conn.RemoteAddr(), port, identifier)
			conn.Close()
			continue
		}
		sourceType := parts[2]
		log.Printf("INFO: Session %s: Connection %s on port %d identified as %s", sessionToken, conn.RemoteAddr(), port, sourceType)

		if sourceType == "CLIENT_APP" {
			if clientAppConn != nil {
				log.Printf("WARN: Session %s: Duplicate CLIENT_APP connection from %s. Closing new one.", sessionToken, conn.RemoteAddr())
				conn.Close()
			} else {
				clientAppConn = conn
			}
		} else if sourceType == "HOST_PROXY" {
			if hostProxyConn != nil {
				log.Printf("WARN: Session %s: Duplicate HOST_PROXY connection from %s. Closing new one.", sessionToken, conn.RemoteAddr())
				conn.Close()
			} else {
				hostProxyConn = conn
			}
		} else {
			log.Printf("WARN: Session %s: Unknown source type '%s' from %s. Closing it.", sessionToken, sourceType, conn.RemoteAddr())
			conn.Close()
		}
	}

	if clientAppConn == nil || hostProxyConn == nil {
		log.Printf("WARN: Session %s: Failed to establish complete data relay on port %d. Client connected: %t, Host proxy connected: %t. Aborting.",
			sessionToken, port, clientAppConn != nil, hostProxyConn != nil)
		if clientAppConn != nil {
			clientAppConn.Close()
		}
		if hostProxyConn != nil {
			hostProxyConn.Close()
		}
		return
	}

	log.Printf("INFO: Session %s: Data connections established on port %d. Relaying between CLIENT_APP (%s) and HOST_PROXY (%s).",
		sessionToken, port, clientAppConn.RemoteAddr(), hostProxyConn.RemoteAddr())

	var relayWg sync.WaitGroup
	relayWg.Add(2)
	go func() {
		defer relayWg.Done()
		defer clientAppConn.Close()
		defer hostProxyConn.Close()
		written, err := io.Copy(hostProxyConn, clientAppConn)
		if err != nil && err != io.EOF && !strings.Contains(err.Error(), "use of closed network connection") {
			log.Printf("ERROR: Session %s: Copying CLIENT_APP to HOST_PROXY: %v (bytes: %d)", sessionToken, err, written)
		} else {
			log.Printf("INFO: Session %s: CLIENT_APP to HOST_PROXY stream ended. Bytes: %d", sessionToken, written)
		}
	}()
	go func() {
		defer relayWg.Done()
		defer hostProxyConn.Close()
		defer clientAppConn.Close()
		written, err := io.Copy(clientAppConn, hostProxyConn)
		if err != nil && err != io.EOF && !strings.Contains(err.Error(), "use of closed network connection") {
			log.Printf("ERROR: Session %s: Copying HOST_PROXY to CLIENT_APP: %v (bytes: %d)", sessionToken, err, written)
		} else {
			log.Printf("INFO: Session %s: HOST_PROXY to CLIENT_APP stream ended. Bytes: %d", sessionToken, written)
		}
	}()
	relayWg.Wait()
	log.Printf("INFO: Session %s: Relaying ended for port %d.", sessionToken, port)
}

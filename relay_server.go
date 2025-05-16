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
		// Handle each control connection in its own goroutine.
		go relay.handleControlConnection(conn)
	}
}

// handleControlConnection processes messages from a control connection (either host sidecar or launcher).
func (r *RelayServer) handleControlConnection(conn net.Conn) {
	defer conn.Close()
	remoteAddr := conn.RemoteAddr().String()
	log.Printf("INFO: New control connection from: %s", remoteAddr)
	reader := bufio.NewReader(conn)

	var registeredHostID string // If this control connection successfully registers a host

	for {
		message, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				log.Printf("ERROR: Reading from control connection %s: %v", remoteAddr, err)
			} else {
				log.Printf("INFO: Control connection %s closed (EOF)", remoteAddr)
			}
			// Cleanup if this was a registered host's control connection
			if registeredHostID != "" {
				r.mu.Lock()
				if r.hostControlConns[registeredHostID] == conn { // Check if it's still the same connection
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
		case "REGISTER_HOST": // Expected from host's sidecar: REGISTER_HOST <host_id>
			if len(parts) < 2 {
				fmt.Fprintln(conn, "ERROR Invalid REGISTER_HOST. Usage: REGISTER_HOST <host_id>")
				continue
			}
			hostID := parts[1]
			r.mu.Lock()
			if _, exists := r.hostControlConns[hostID]; exists {
				// Potentially close the old connection or disallow if active.
				// For now, allow re-registration to update the connection.
				log.Printf("WARN: Host ID '%s' re-registering. Old control conn: %s, New: %s", hostID, r.hostControlConns[hostID].RemoteAddr(), remoteAddr)
				// r.hostControlConns[hostID].Close() // Optionally close the old one
			}
			r.hostControlConns[hostID] = conn
			registeredHostID = hostID // Mark this connection as belonging to this hostID
			r.mu.Unlock()
			log.Printf("INFO: Host '%s' registered with control connection %s", hostID, remoteAddr)
			fmt.Fprintf(conn, "HOST_REGISTERED %s\n", hostID)

		case "INITIATE_CLIENT_SESSION": // Expected from launcher: INITIATE_CLIENT_SESSION <target_host_id>
			if len(parts) < 2 {
				fmt.Fprintln(conn, "ERROR Invalid INITIATE_CLIENT_SESSION. Usage: INITIATE_CLIENT_SESSION <target_host_id>")
				continue
			}
			targetHostID := parts[1]

			r.mu.Lock()
			hostControlConn, hostIsRegistered := r.hostControlConns[targetHostID]
			r.mu.Unlock() // Unlock early before potentially blocking operations like net.Listen

			if !hostIsRegistered {
				log.Printf("WARN: Target host '%s' not found for client session from %s", targetHostID, remoteAddr)
				fmt.Fprintf(conn, "ERROR_HOST_NOT_FOUND %s\n", targetHostID)
				continue
			}

			sessionToken := uuid.New().String()
			dataListener, err := net.Listen("tcp", ":0") // OS chooses a free port for data
			if err != nil {
				log.Printf("ERROR: Failed to create dynamic data listener for session %s: %v", sessionToken, err)
				fmt.Fprintf(conn, "ERROR_RELAY_INTERNAL Failed to create data port\n")
				continue
			}
			dynamicRelayAddr := dataListener.Addr().String()
			log.Printf("INFO: Session %s for host '%s': Dynamic data listener at %s", sessionToken, targetHostID, dynamicRelayAddr)

			// Tell client (launcher) where client.exe should connect
			fmt.Fprintf(conn, "SESSION_READY %s %s\n", dynamicRelayAddr, sessionToken)
			log.Printf("INFO: Session %s: Notified launcher %s to have client.exe connect to %s", sessionToken, remoteAddr, dynamicRelayAddr)

			// Tell target host (via its control conn) to connect its proxy to the dynamic data port
			// Ensure hostControlConn is still valid and active before writing
			if hostControlConn != nil {
				_, errSend := fmt.Fprintf(hostControlConn, "CREATE_TUNNEL %s %s\n", dynamicRelayAddr, sessionToken)
				if errSend != nil {
					log.Printf("ERROR: Session %s: Failed to send CREATE_TUNNEL to host '%s' (%s): %v. Aborting session.", sessionToken, targetHostID, hostControlConn.RemoteAddr(), errSend)
					fmt.Fprintf(conn, "ERROR_RELAY_INTERNAL Failed to notify host. Host might be disconnected.\n")
					dataListener.Close() // Clean up the listener
					// Consider removing host from registry if its control conn failed
					r.mu.Lock()
					if r.hostControlConns[targetHostID] == hostControlConn { // If it's the same problematic conn
						delete(r.hostControlConns, targetHostID)
						log.Printf("INFO: Removed host '%s' due to control connection error during session setup.", targetHostID)
					}
					r.mu.Unlock()
					continue // Abort this session setup
				}
				log.Printf("INFO: Session %s: Notified host '%s' (control %s) to create tunnel to %s", sessionToken, targetHostID, hostControlConn.RemoteAddr(), dynamicRelayAddr)
			} else { // Should not happen if hostIsRegistered and hostControlConn was retrieved
				log.Printf("CRITICAL: Session %s: Host '%s' was registered but its control connection is nil. Aborting.", sessionToken, targetHostID)
				fmt.Fprintf(conn, "ERROR_RELAY_INTERNAL Host control connection issue.\n")
				dataListener.Close()
				continue
			}

			// Goroutine to wait for client.exe and host's data proxy connection on the dynamic port
			go r.manageDataSession(dataListener, sessionToken, targetHostID)
			// After sending SESSION_READY, this control connection from the launcher has served its purpose for this request.
			// It could be closed or kept open for more requests from the same launcher.
			// For simplicity, we'll let the loop continue. If launcher closes, EOF will handle it.

		default:
			log.Printf("WARN: Unknown control command from %s: '%s'", remoteAddr, message)
			fmt.Fprintf(conn, "ERROR Unknown command: %s\n", command)
		}
	}
}

// manageDataSession waits for two connections (one from client.exe, one from host's proxy)
// on the dataListener, identifies them using the sessionToken, and then relays traffic.
func (r *RelayServer) manageDataSession(dataListener net.Listener, sessionToken string, hostID string) {
	defer dataListener.Close()
	log.Printf("INFO: Session %s (Host '%s'): Waiting for data connections on %s (timeout: %s)", sessionToken, hostID, dataListener.Addr(), dataConnTimeout*2) // Total timeout for both

	var clientAppConn, hostProxyConn net.Conn
	var wg sync.WaitGroup
	wg.Add(2) // Expecting two connections

	connChan := make(chan net.Conn, 2)
	errChan := make(chan error, 2) // To catch errors from Accept

	// Goroutine to accept the first connection
	go func() {
		defer wg.Done()
		conn, err := dataListener.Accept()
		if err != nil {
			errChan <- fmt.Errorf("accept1 failed: %w", err)
			return
		}
		connChan <- conn
	}()

	// Goroutine to accept the second connection
	go func() {
		defer wg.Done()
		conn, err := dataListener.Accept()
		if err != nil {
			errChan <- fmt.Errorf("accept2 failed: %w", err)
			return
		}
		connChan <- conn
	}()

	// Wait for both Accept goroutines to finish or timeout
	acceptTimeout := time.After(dataConnTimeout * 2) // Total time to get both connections
	var acceptedConns []net.Conn

LoopAccept:
	for i := 0; i < 2; i++ {
		select {
		case conn := <-connChan:
			acceptedConns = append(acceptedConns, conn)
		case err := <-errChan:
			// Check if the error is due to listener being closed, which might happen if session is aborted elsewhere
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				log.Printf("WARN: Session %s: Temporary error accepting data connection: %v. Retrying accept might be needed if not closing.", sessionToken, err)
				i-- // Potentially retry, though simple Accept doesn't retry.
				continue
			}
			if strings.Contains(err.Error(), "use of closed network connection") {
				log.Printf("INFO: Session %s: Data listener was closed while waiting for connections.", sessionToken)
			} else {
				log.Printf("ERROR: Session %s: Error accepting data connection: %v", sessionToken, err)
			}
			break LoopAccept // Stop trying to accept
		case <-acceptTimeout:
			log.Printf("WARN: Session %s: Timed out waiting for all data connections.", sessionToken)
			break LoopAccept
		}
	}

	wg.Wait() // Ensure Accept goroutines complete

	// Identify the accepted connections
	for _, conn := range acceptedConns {
		conn.SetReadDeadline(time.Now().Add(identTimeout)) // Deadline for sending identification
		identifier, err := bufio.NewReader(conn).ReadString('\n')
		conn.SetReadDeadline(time.Time{}) // Clear deadline

		if err != nil {
			log.Printf("WARN: Session %s: Failed to read identification from %s: %v. Closing it.", sessionToken, conn.RemoteAddr(), err)
			conn.Close()
			continue
		}

		identifier = strings.TrimSpace(identifier)
		parts := strings.Fields(identifier) // Expect: SESSION_TOKEN <token> <CLIENT_APP|HOST_PROXY>

		if len(parts) != 3 || parts[0] != "SESSION_TOKEN" || parts[1] != sessionToken {
			log.Printf("WARN: Session %s: Invalid identification from %s: '%s'. Closing it.", sessionToken, conn.RemoteAddr(), identifier)
			conn.Close()
			continue
		}

		sourceType := parts[2]
		log.Printf("INFO: Session %s: Connection %s identified as %s", sessionToken, conn.RemoteAddr(), sourceType)

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

	// Check if we got both connections identified correctly
	if clientAppConn == nil || hostProxyConn == nil {
		log.Printf("WARN: Session %s: Failed to establish complete data relay. Client connected: %t, Host proxy connected: %t. Aborting session.",
			sessionToken, clientAppConn != nil, hostProxyConn != nil)
		if clientAppConn != nil {
			clientAppConn.Close()
		}
		if hostProxyConn != nil {
			hostProxyConn.Close()
		}
		return
	}

	log.Printf("INFO: Session %s: Data connections established. Relaying between CLIENT_APP (%s) and HOST_PROXY (%s).",
		sessionToken, clientAppConn.RemoteAddr(), hostProxyConn.RemoteAddr())

	// Start relaying traffic
	var relayWg sync.WaitGroup
	relayWg.Add(2)

	go func() {
		defer relayWg.Done()
		defer clientAppConn.Close()
		defer hostProxyConn.Close()
		written, err := io.Copy(hostProxyConn, clientAppConn) // client -> host
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
		written, err := io.Copy(clientAppConn, hostProxyConn) // host -> client
		if err != nil && err != io.EOF && !strings.Contains(err.Error(), "use of closed network connection") {
			log.Printf("ERROR: Session %s: Copying HOST_PROXY to CLIENT_APP: %v (bytes: %d)", sessionToken, err, written)
		} else {
			log.Printf("INFO: Session %s: HOST_PROXY to CLIENT_APP stream ended. Bytes: %d", sessionToken, written)
		}
	}()

	relayWg.Wait()
	log.Printf("INFO: Session %s: Relaying ended.", sessionToken)
}

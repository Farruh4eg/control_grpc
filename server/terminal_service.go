package main

import (
	_ "embed"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	pb "control_grpc/gen/proto"
	"github.com/iamacarpet/go-winpty"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

//go:embed winpty.dll
var winptyDllEmbed []byte

//go:embed winpty-agent.exe
var winptyAgentEmbed []byte

var (
	winptyInitOnce sync.Once
	winptyInitErr  error
)

func extractFileToPath(outputPath string, data []byte, perm os.FileMode) error {
	log.Printf("INFO: Ensuring presence of %s by writing/overwriting...", outputPath)
	err := os.WriteFile(outputPath, data, perm)
	if err != nil {
		return fmt.Errorf("failed to write embedded file to %s: %w", outputPath, err)
	}
	log.Printf("INFO: Successfully wrote/updated %s (%d bytes).", outputPath, len(data))
	return nil
}

// ensureWinptyBinariesAreExtracted extracts the embedded winpty.dll and winpty-agent.exe
func ensureWinptyBinariesAreExtracted() error {
	winptyInitOnce.Do(func() {
		log.Println("INFO: Performing one-time extraction check for WinPTY binaries...")
		exePath, err := os.Executable()
		if err != nil {
			winptyInitErr = fmt.Errorf("failed to get executable path: %w", err)
			return
		}
		exeDir := filepath.Dir(exePath)
		log.Printf("INFO: Current executable directory for WinPTY extraction: %s", exeDir)

		dllPath := filepath.Join(exeDir, "winpty.dll")
		agentPath := filepath.Join(exeDir, "winpty-agent.exe")

		if len(winptyDllEmbed) == 0 {
			log.Println("WARN: Embedded winpty.dll data is empty. Cannot extract.")
			// Optionally set winptyInitErr here if this is critical
		} else {
			if err := extractFileToPath(dllPath, winptyDllEmbed, 0644); err != nil {
				winptyInitErr = fmt.Errorf("failed to extract winpty.dll: %w", err)
				return
			}
		}

		if len(winptyAgentEmbed) == 0 {
			log.Println("WARN: Embedded winpty-agent.exe data is empty. Cannot extract.")
			// Optionally set winptyInitErr here
		} else {
			if err := extractFileToPath(agentPath, winptyAgentEmbed, 0755); err != nil {
				winptyInitErr = fmt.Errorf("failed to extract winpty-agent.exe: %w", err)
				return
			}
		}
		if winptyInitErr == nil {
			log.Println("INFO: WinPTY binaries successfully checked/extracted.")
		}
	})
	return winptyInitErr
}

var ansiEscapePattern = regexp.MustCompile(`(\x1b\[\??[0-9;]*[a-zA-Z])|(\x1b\][^\a]*\a)|\x07`)

func stripANSI(str string) string {
	return ansiEscapePattern.ReplaceAllString(str, "")
}

// CommandStream handles bidirectional terminal commands and output using WinPTY.
func (s *server) CommandStream(stream pb.TerminalService_CommandStreamServer) error {
	log.Println("TerminalService (WinPTY): Client connected to CommandStream.")

	if err := ensureWinptyBinariesAreExtracted(); err != nil {
		log.Printf("TerminalService (WinPTY): Critical error ensuring WinPTY binaries: %v", err)
		return status.Errorf(codes.FailedPrecondition, "failed to prepare WinPTY environment: %v", err)
	}

	ctx := stream.Context()

	initialCwd, err := os.Getwd()
	if err != nil {
		homeDir, homeErr := os.UserHomeDir()
		if homeErr != nil {
			log.Printf("TerminalService (WinPTY): Error getting CWD (%v) and home dir (%v). Using OS default.", err, homeErr)
			if runtime.GOOS == "windows" {
				initialCwd = "C:\\"
			} else {
				initialCwd = "/"
			}
		} else {
			initialCwd = homeDir
		}
	}
	log.Printf("TerminalService (WinPTY): Initializing WinPTY session in directory: %s", initialCwd)

	var shellCmdArgs []string
	var shellPath string

	if runtime.GOOS == "windows" {
		psPath, errPs := exec.LookPath("powershell.exe")
		if errPs == nil {
			shellPath = psPath
			shellCmdArgs = []string{"-NoProfile"}
			log.Printf("TerminalService (WinPTY): Using PowerShell at %s with args: %v", shellPath, shellCmdArgs)
		} else {
			cmdPath, errCmd := exec.LookPath("cmd.exe")
			if errCmd == nil {
				shellPath = cmdPath
				shellCmdArgs = []string{}
				log.Printf("TerminalService (WinPTY): PowerShell not found, falling back to CMD at %s", shellPath)
			} else {
				log.Printf("TerminalService (WinPTY): Error - Neither PowerShell nor CMD found. PowerShell err: %v, CMD err: %v", errPs, errCmd)
				return status.Errorf(codes.FailedPrecondition, "no suitable shell found on Windows server (PowerShell or CMD)")
			}
		}
	} else {
		log.Printf("TerminalService (WinPTY): Error - go-winpty is intended for Windows. Current OS: %s", runtime.GOOS)
		return status.Errorf(codes.FailedPrecondition, "go-winpty is for Windows only, server OS is %s", runtime.GOOS)
	}

	var fullCmdLineBuilder strings.Builder
	fullCmdLineBuilder.WriteString(shellPath)
	for _, arg := range shellCmdArgs {
		fullCmdLineBuilder.WriteString(" ")
		if strings.Contains(arg, " ") {
			fullCmdLineBuilder.WriteString("\"")
			fullCmdLineBuilder.WriteString(arg)
			fullCmdLineBuilder.WriteString("\"")
		} else {
			fullCmdLineBuilder.WriteString(arg)
		}
	}
	fullCmdLine := fullCmdLineBuilder.String()

	log.Printf("TerminalService (WinPTY): Preparing to start WinPTY with command line: '%s'", fullCmdLine)

	ptyOptions := &winpty.Options{
		Command: fullCmdLine,
		Dir:     initialCwd,
		Env:     os.Environ(),
		Flags: winpty.WINPTY_SPAWN_FLAG_AUTO_SHUTDOWN |
			winpty.WINPTY_FLAG_ALLOW_CURPROC_DESKTOP_CREATION,
		InitialCols: 120,
		InitialRows: 30,
	}

	pty, err := winpty.OpenWithOptions(*ptyOptions)
	if err != nil {
		log.Printf("TerminalService (WinPTY): Error starting WinPTY with command '%s': %v. Ensure winpty.dll and winpty-agent.exe are accessible in the executable's directory.", fullCmdLine, err)
		return status.Errorf(codes.Internal, "failed to start WinPTY: %v. Check server logs for extraction details.", err)
	}
	log.Printf("TerminalService (WinPTY): WinPTY started successfully for shell: %s", shellPath)

	defer func() {
		log.Println("TerminalService (WinPTY): Cleaning up WinPTY session...")
		if pty != nil {
			pty.StdIn.Close()
			pty.Close()
			log.Println("TerminalService (WinPTY): pty.Close() called.")
		}
		log.Println("TerminalService (WinPTY): WinPTY session cleanup complete.")
	}()

	var ptyReadWg sync.WaitGroup
	ptyReadWg.Add(1)

	go func() {
		defer ptyReadWg.Done()
		log.Println("TerminalService (WinPTY): PTY stdout read goroutine started.")
		buf := make([]byte, 8192)
		for {
			select {
			case <-ctx.Done():
				log.Printf("TerminalService (WinPTY): PTY stdout read goroutine: stream context done: %v. Exiting.", ctx.Err())
				return
			default:
			}

			n, readErr := pty.StdOut.Read(buf)
			if n > 0 {
				outputData := buf[:n]
				var outputToSend string

				if utf8.Valid(outputData) {
					rawString := string(outputData)
					outputToSend = stripANSI(rawString)
				} else {
					outputToSend = "\n"
					log.Printf("TerminalService (WinPTY): Invalid UTF-8 detected in PTY output chunk. Sending newline. Original (hex): %x", outputData)
				}

				if sendErr := stream.Send(&pb.TerminalResponse{
					OutputType:   pb.TerminalResponse_STDOUT,
					OutputLine:   outputToSend,
					CommandEnded: false,
				}); sendErr != nil {
					log.Printf("TerminalService (WinPTY): Error sending PTY stdout to client: %v. Exiting read goroutine.", sendErr)
					return
				}
			}

			if readErr != nil {
				finalMsg := "--- PTY session ended (stdout) ---"
				if readErr == io.EOF {
					log.Println("TerminalService (WinPTY): EOF reading from PTY stdout. Shell process likely exited.")
				} else {
					if ctx.Err() == nil {
						log.Printf("TerminalService (WinPTY): Error reading from PTY stdout: %v", readErr)
						finalMsg = fmt.Sprintf("--- PTY read error: %v ---", readErr)
					} else {
						log.Printf("TerminalService (WinPTY): PTY stdout read error after context cancellation: %v", readErr)
						finalMsg = fmt.Sprintf("--- PTY session ended (context done, stdout read err: %v) ---", ctx.Err())
					}
				}
				_ = stream.Send(&pb.TerminalResponse{OutputLine: finalMsg, CommandEnded: true})
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			log.Printf("TerminalService (WinPTY): Main loop: stream context done: %v. Waiting for PTY read goroutine to finish.", ctx.Err())
			ptyReadWg.Wait()
			log.Println("TerminalService (WinPTY): Main loop: PTY read goroutine finished. Exiting CommandStream.")
			return ctx.Err()
		default:
		}

		req, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				log.Println("TerminalService (WinPTY): Client closed send stream (EOF). PTY session will continue until explicitly closed or shell exits.")
			} else {
				st, ok := status.FromError(err)
				if ok && (st.Code() == codes.Canceled || st.Code() == codes.Unavailable) {
					log.Printf("TerminalService (WinPTY): Client disconnected or stream unavailable: %v", err)
				} else {
					log.Printf("TerminalService (WinPTY): Error receiving input from client: %v", err)
				}
			}

			if err != io.EOF {
				log.Printf("TerminalService (WinPTY): Non-EOF error on Recv: %v. Terminating session.", err)
				ptyReadWg.Wait()
				return err
			}

			log.Println("TerminalService (WinPTY): Client stopped sending (EOF on Recv). PTY output stream remains active.")
			<-ctx.Done()
			log.Println("TerminalService (WinPTY): Context cancelled after client Recv EOF. Terminating session.")
			ptyReadWg.Wait()
			return ctx.Err()
		}

		inputFromClient := req.GetCommand()
		inputBytes := []byte(inputFromClient + "\r\n")

		if _, writeErr := pty.StdIn.Write(inputBytes); writeErr != nil {
			log.Printf("TerminalService (WinPTY): Error writing to PTY stdin: %v", writeErr)
			_ = stream.Send(&pb.TerminalResponse{
				OutputType:   pb.TerminalResponse_ERROR_MESSAGE,
				OutputLine:   fmt.Sprintf("--- Error writing to PTY: %v ---", writeErr),
				CommandEnded: true,
			})
			ptyReadWg.Wait() // Wait for reader to finish
			return status.Errorf(codes.Internal, "failed to write to PTY stdin: %v", writeErr)
		}
	}
}

func firstNBytes(data []byte, n int) []byte {
	if len(data) > n {
		return data[:n]
	}
	return data
}

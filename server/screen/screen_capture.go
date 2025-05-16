package screen

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/StackExchange/wmi"
	"github.com/kbinani/screenshot" // For getting display dimensions
)

type ScreenCapture struct {
	cmd       *exec.Cmd
	output    io.ReadCloser
	stderr    bytes.Buffer // Buffer to capture stderr
	restartCh chan struct{}
	mu        sync.Mutex
	running   bool
}

type Win32_VideoController struct {
	Name string
}

var Accel string

func NewScreenCapture() (*ScreenCapture, error) {
	sc := &ScreenCapture{
		restartCh: make(chan struct{}, 1),
		running:   true,
	}

	var err error
	Accel, err = detectEncoder()
	if err != nil {
		log.Printf("Error detecting encoder: %v. Will use fallback in start().", err)
	}

	if err := sc.start(); err != nil {
		return nil, err
	}

	go sc.monitor()
	return sc, nil
}

func (sc *ScreenCapture) start() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	currentEncoder := Accel
	if currentEncoder == "" {
		var detectErr error
		currentEncoder, detectErr = detectEncoder()
		if detectErr != nil {
			log.Printf("Screen capture start: Error detecting encoder: %v. Fallback to libx264.", detectErr)
			currentEncoder = "libx264"
		}
	}
	log.Printf("Screen capture: Attempting to use encoder: %s", currentEncoder)

	// --- Get primary monitor dimensions ---
	primaryDisplayIndex := 0 // Assuming primary display is at index 0
	bounds := screenshot.GetDisplayBounds(primaryDisplayIndex)

	captureWidth := bounds.Dx()
	captureHeight := bounds.Dy()
	captureOffsetX := bounds.Min.X
	captureOffsetY := bounds.Min.Y

	if captureWidth <= 0 || captureHeight <= 0 {
		log.Printf("Screen capture: Invalid primary display dimensions (%dx%d). Capturing entire desktop and scaling.", captureWidth, captureHeight)
		// Fallback to capturing whole desktop and just scaling
		// Video filter will only contain scale and format
	}
	log.Printf("Screen capture: Primary display detected as %dx%d at offset (%d,%d)", captureWidth, captureHeight, captureOffsetX, captureOffsetY)

	// Base arguments for FFmpeg
	args := []string{
		"-f", "gdigrab",
		// No -video_size, -offset_x, -offset_y here for gdigrab input.
		// gdigrab will capture the entire virtual desktop.
		// The crop filter will select the primary monitor area from this.
		"-framerate", "30",
		"-i", "desktop",
		"-an", // No audio
		// Video filter chain: crop, then scale, then set pixel format
		// If captureWidth/Height are invalid, we'll construct a simpler vf
	}

	// Construct video filter string
	vfString := ""
	if captureWidth > 0 && captureHeight > 0 {
		vfString = fmt.Sprintf("crop=%d:%d:%d:%d,scale=1920:1080,format=yuv420p",
			captureWidth, captureHeight, captureOffsetX, captureOffsetY)
	} else {
		// Fallback if display bounds were not correctly determined
		vfString = "scale=1920:1080,format=yuv420p"
	}
	args = append(args, "-vf", vfString)

	args = append(args,
		"-c:v", currentEncoder,
		"-g", "60", // GOP size
		"-flags", "+low_delay",
		"-fflags", "nobuffer",
		"-f", "mpegts",
		"-flush_packets", "1",
		"pipe:1", // Output to stdout
	)

	// Hardware-specific options
	switch {
	case strings.Contains(currentEncoder, "nvenc"):
		args = append(args,
			"-preset", "ll",
			"-profile:v", "high",
			"-rc", "vbr_hq",
			"-b:v", "3M",
			"-maxrate", "5M",
			"-bufsize", "6M",
			"-multipass", "0",
			"-delay", "0",
			"-zerolatency", "1", // This flag might cause issues on some FFmpeg/driver versions. Remove if problematic.
			"-rc-lookahead", "0",
			"-forced-idr", "1",
			"-strict", "2",
		)

	case strings.Contains(currentEncoder, "amf"):
		args = append(args,
			"-usage", "ultralowlatency",
			"-quality", "speed",
			"-profile:v", "high",
			"-rc", "cbr",
			"-b:v", "3M",
		)

	case strings.Contains(currentEncoder, "qsv"):
		args = append(args,
			"-preset", "veryfast",
			"-profile:v", "high",
			"-look_ahead", "0",
			"-async_depth", "1",
			"-b:v", "3M",
			"-maxrate", "5M",
		)

	case strings.Contains(currentEncoder, "libx264"):
		args = append(args,
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			// "-crf", "23", // Using CRF is generally better for quality with libx264
			// Forcing bitrate for now for consistency with other encoders
			"-b:v", "3M", "-maxrate", "4M", "-bufsize", "6M",
		)
	default:
		log.Printf("Screen capture: Encoder '%s' not specifically handled, using generic libx264 settings.", currentEncoder)
		args = replaceOrAddArg(args, "-c:v", "libx264") // Ensure codec is set
		args = append(args,
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-b:v", "2M", "-maxrate", "3M", "-bufsize", "4M", // More conservative bitrate for fallback
		)
	}

	log.Printf("Screen capture: Starting FFmpeg with args: %v", args)

	sc.cmd = exec.Command("ffmpeg", args...)
	sc.stderr.Reset()
	sc.cmd.Stderr = &sc.stderr

	var startErr error
	sc.output, startErr = sc.cmd.StdoutPipe()
	if startErr != nil {
		log.Printf("Screen capture: Error creating StdoutPipe: %v. Stderr: %s", startErr, sc.stderr.String())
		return startErr
	}

	if startErr = sc.cmd.Start(); startErr != nil {
		log.Printf("Screen capture: Error starting FFmpeg: %v. Stderr: %s", startErr, sc.stderr.String())
		if sc.output != nil {
			io.Copy(io.Discard, sc.output) // Drain pipe
			sc.output.Close()
			sc.output = nil
		}
		return startErr
	}

	log.Printf("Screen capture started with %s encoder (PID: %d)", currentEncoder, sc.cmd.Process.Pid)

	go func() {
		waitErr := sc.cmd.Wait()
		sc.mu.Lock()
		if sc.running {
			log.Printf("Screen capture: FFmpeg process (PID: %d) exited while capture was expected to be running. Error: %v. Stderr: %s", sc.cmd.Process.Pid, waitErr, sc.stderr.String())
			if sc.output != nil {
				sc.output.Close()
				sc.output = nil
			}
			select {
			case sc.restartCh <- struct{}{}:
			default:
				log.Println("Screen capture: restartCh is full or monitor not ready, restart signal might be missed.")
			}
		} else {
			log.Printf("Screen capture: FFmpeg process (PID: %d) exited (expected due to Close call or failed restart). Error (if any): %v. Stderr: %s", sc.cmd.Process.Pid, waitErr, sc.stderr.String())
		}
		sc.mu.Unlock()
	}()

	return nil
}

// Helper function to replace or add an argument and its value
func replaceOrAddArg(args []string, argToSet string, valueToSet string) []string {
	found := false
	for i, arg := range args {
		if arg == argToSet {
			if i+1 < len(args) { // If it's not the last argument, assume next is its value
				args[i+1] = valueToSet
				found = true
				break
			} else { // Argument is last, needs value appended
				args = append(args, valueToSet) // This case might be wrong if argToSet is a flag without value
				found = true
				break
			}
		}
	}
	if !found {
		args = append(args, argToSet, valueToSet)
	}
	return args
}

func detectEncoder() (string, error) {
	var controllers []Win32_VideoController
	query := "SELECT Name FROM Win32_VideoController"
	done := make(chan error, 1)
	go func() {
		done <- wmi.Query(query, &controllers)
	}()

	select {
	case err := <-done:
		if err != nil {
			log.Printf("WMI query error: %v", err)
			return "libx264", errors.New("WMI query failed, fallback to software encoder")
		}
		if len(controllers) == 0 {
			return "libx264", errors.New("no video controllers found via WMI, fallback to software encoder")
		}
	case <-time.After(5 * time.Second):
		log.Println("WMI query timed out, fallback to software encoder")
		return "libx264", errors.New("WMI query timed out, fallback to software encoder")
	}

	gpuVendor := strings.ToLower(controllers[0].Name)
	log.Printf("Detected GPU: %s", controllers[0].Name)
	switch {
	case strings.Contains(gpuVendor, "amd"):
		return "h264_amf", nil
	case strings.Contains(gpuVendor, "nvidia"):
		return "h264_nvenc", nil
	case strings.Contains(gpuVendor, "intel"):
		return "h264_qsv", nil
	default:
		log.Printf("Unknown GPU vendor: %s, fallback to software encoder", gpuVendor)
		return "libx264", nil
	}
}

func (sc *ScreenCapture) monitor() {
	for {
		select {
		case _, ok := <-sc.restartCh:
			if !ok {
				log.Println("Screen capture: restartCh closed, monitor exiting.")
				return
			}
			sc.mu.Lock()
			shouldRestart := sc.running
			sc.mu.Unlock()

			if shouldRestart {
				log.Println("Screen capture: Received restart signal. Attempting to restart...")
				sc.cleanupInternal()
				restarted := false
				for retry := 0; retry < 3; retry++ {
					log.Printf("Screen capture: Restart attempt #%d", retry+1)
					if err := sc.start(); err == nil {
						log.Println("Screen capture: Successfully restarted.")
						restarted = true
						break
					}
					log.Printf("Screen capture: Failed to restart, attempt #%d. Retrying in 2 seconds...", retry+1)
					time.Sleep(time.Second * 2)
				}
				if !restarted {
					log.Println("Screen capture: Failed to restart after multiple attempts. Stopping monitor.")
					sc.mu.Lock()
					sc.running = false
					sc.mu.Unlock()
					return
				}
			} else {
				log.Println("Screen capture: Received restart signal, but not running. Ignoring.")
			}
		default:
			time.Sleep(500 * time.Millisecond)
			sc.mu.Lock()
			if !sc.running {
				sc.mu.Unlock()
				log.Println("Screen capture: Monitor detected not running, exiting.")
				return
			}
			sc.mu.Unlock()
		}
	}
}

func (sc *ScreenCapture) ReadFrame(buffer []byte) (int, error) {
	sc.mu.Lock()
	if sc.output == nil {
		sc.mu.Unlock()
		log.Println("Screen capture: ReadFrame called but output pipe is nil. Returning EOF.")
		return 0, io.EOF
	}
	currentOutput := sc.output
	sc.mu.Unlock()

	n, err := currentOutput.Read(buffer)

	if err != nil {
		// Log the error, but the restart logic is primarily handled by the cmd.Wait() goroutine
		log.Printf("Screen capture: Error reading frame: %v (read %d bytes)", err, n)
		return n, io.EOF // Propagate EOF or error to caller, signaling them to stop
	}
	return n, nil
}

// cleanupInternal is called to clean up resources. Assumes sc.mu might be held by caller or needs to be managed carefully.
func (sc *ScreenCapture) cleanupInternal() {
	// This function might be called from Close() (which holds lock)
	// or from monitor() (which doesn't hold lock during this call).
	// For simplicity, let's assume it's okay if Kill is called multiple times or on an already exited process.
	if sc.cmd != nil && sc.cmd.Process != nil {
		log.Printf("Screen capture: Attempting to kill FFmpeg process (PID: %d)...", sc.cmd.Process.Pid)
		err := sc.cmd.Process.Kill()
		if err != nil {
			// Log error, but don't stop cleanup. Process might have already exited.
			log.Printf("Screen capture: Error killing FFmpeg process: %v", err)
		}
		// sc.cmd.Wait() // The goroutine in start() is responsible for Wait()
	}
	if sc.output != nil {
		log.Println("Screen capture: Closing output pipe...")
		sc.output.Close()
		sc.output = nil // Mark as closed
	}
}

func (sc *ScreenCapture) Close() {
	log.Println("Screen capture: Close called.")
	sc.mu.Lock()
	if !sc.running {
		sc.mu.Unlock()
		log.Println("Screen capture: Already closed or closing.")
		return
	}
	sc.running = false // Signal monitor and other parts to stop

	// Safely close restartCh
	if sc.restartCh != nil {
		select {
		case <-sc.restartCh: // Drain if anything was sent, though unlikely
		default:
		}
		close(sc.restartCh)
		sc.restartCh = nil
	}

	// Call cleanupInternal while lock is held
	sc.cleanupInternal()
	sc.mu.Unlock()
	log.Println("Screen capture: Resources cleaned up after Close call.")
}

package screen

import (
	"errors"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/StackExchange/wmi"
)

type ScreenCapture struct {
	cmd       *exec.Cmd
	output    io.ReadCloser
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

	if err := sc.start(); err != nil {
		return nil, err
	}

	go sc.monitor()
	return sc, nil
}

func (sc *ScreenCapture) start() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	Accel, err := detectEncoder()
	if err != nil {
		return err
	}

	args := []string{
		"-f", "gdigrab",
		"-framerate", "30",
		"-i", "desktop",
		"-an",
		"-vf", "scale=1920:1080",
		"-c:v", Accel,
		"-b:v", "3M",
		"-g", "30", // Smaller GOP size
		"-tune", "zerolatency", // Zero latency tuning
		"-flags", "+low_delay", // Low delay flags
		"-fflags", "nobuffer", // Reduce buffering
		"-f", "mpegts",
		"-flush_packets", "1",
		"pipe:1",
	}

	// Add hardware-specific low latency options
	switch {
	case strings.Contains(Accel, "nvenc"):
		args = append(args, "-rc-lookahead", "0", "-strict", "2")
	case strings.Contains(Accel, "amf"):
		args = append(args, "-usage", "lowlatency")
	case strings.Contains(Accel, "qsv"):
		args = append(args, "-async_depth", "1")
	}

	sc.cmd = exec.Command("ffmpeg", args...)

	sc.output, err = sc.cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := sc.cmd.Start(); err != nil {
		return err
	}

	log.Printf("Screen capture started with %s encoder", Accel)
	return nil
}

func detectEncoder() (string, error) {
	var controllers []Win32_VideoController
	query := "SELECT Name FROM Win32_VideoController"
	if err := wmi.Query(query, &controllers); err != nil || len(controllers) == 0 {
		return "libx264", errors.New("fallback to software encoder")
	}

	gpuVendor := strings.ToLower(controllers[0].Name)
	switch {
	case strings.Contains(gpuVendor, "amd"):
		return "h264_amf", nil
	case strings.Contains(gpuVendor, "nvidia"):
		return "h264_nvenc", nil
	case strings.Contains(gpuVendor, "intel"):
		return "h264_qsv", nil
	default:
		return "libx264", nil
	}
}

func (sc *ScreenCapture) monitor() {
	for sc.running {
		select {
		case <-sc.restartCh:
			log.Println("Attempting to restart screen capture...")
			sc.cleanup()
			for retry := 0; retry < 3; retry++ {
				if err := sc.start(); err == nil {
					break
				}
				time.Sleep(time.Second * 2)
			}
		}
	}
}

func (sc *ScreenCapture) ReadFrame(buffer []byte) (int, error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.output == nil {
		return 0, io.EOF
	}

	n, err := sc.output.Read(buffer)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		sc.restartCh <- struct{}{}
		return 0, io.EOF
	}
	return n, err
}

func (sc *ScreenCapture) Close() {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.running = false
	sc.cleanup()
}

func (sc *ScreenCapture) cleanup() {
	if sc.cmd != nil && sc.cmd.Process != nil {
		sc.cmd.Process.Kill()
	}
	if sc.output != nil {
		sc.output.Close()
	}
}

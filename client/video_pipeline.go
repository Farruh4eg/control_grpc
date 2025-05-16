package main

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"log"
	"time"

	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// Global channels for video processing pipeline
var (
	frameImageData = make(chan image.Image, 30)
	rawFrameBuffer = make(chan []byte, 10)
)

const (
	videoWidth     = 1920
	videoHeight    = 1080
	bytesPerPixel  = 4 // RGBA
	frameSizeBytes = videoWidth * videoHeight * bytesPerPixel
)

// runFFmpegProcess starts and manages the FFmpeg process for video decoding.
func runFFmpegProcess(ffmpegInputReader *io.PipeReader, ffmpegOutputWriter *io.PipeWriter) {
	log.Println("FFmpeg process starting...")
	defer log.Println("FFmpeg process stopped.")
	defer ffmpegInputReader.Close()
	defer ffmpegOutputWriter.Close()

	for {
		stderr := &bytes.Buffer{}
		err := ffmpeg.Input("pipe:0", ffmpeg.KwArgs{
			"format":             "mpegts",
			"flags":              "low_delay",
			"fflags":             "nobuffer+discardcorrupt",
			"protocol_whitelist": "pipe",
			"probesize":          "32K",
			"analyzeduration":    "500000",
		}).
			Output("pipe:1", ffmpeg.KwArgs{
				"format":    "rawvideo",
				"pix_fmt":   "rgba",
				"s":         fmt.Sprintf("%dx%d", videoWidth, videoHeight),
				"flags":     "low_delay",
				"fflags":    "+nobuffer",
				"avioflags": "direct",
			}).
			OverWriteOutput().
			WithInput(ffmpegInputReader).
			WithOutput(ffmpegOutputWriter).
			WithErrorOutput(stderr).
			Run()

		if err != nil {
			log.Printf("FFmpeg process error: %v\nFFmpeg stderr: %s", err, stderr.String())
			time.Sleep(2 * time.Second)
			log.Println("Attempting to restart FFmpeg process (if input pipe is still valid)...")
			// If the input pipe is permanently broken (e.g., network stream ended),
			// FFmpeg will keep failing. The goroutine feeding the input pipe (forwardVideoFeed)
			// is responsible for handling the lifecycle of that input.
			// If FFmpeg cannot start or run, this goroutine might exit or loop.
			// For robustness, if critical errors persist, this goroutine should probably exit.
			return // Exit if FFmpeg cannot run, allowing higher-level logic to handle.
		}
		log.Println("FFmpeg process exited cleanly (unexpected for a continuous stream). Restarting.")
		time.Sleep(1 * time.Second)
	}
}

// readFFmpegOutputToBuffer reads raw video frames from FFmpeg's output pipe
// and sends them as byte slices to the rawFrameBuffer channel.
func readFFmpegOutputToBuffer(ffmpegOutputReader *io.PipeReader, rawFrameBuffer chan []byte) {
	log.Println("FFmpeg output reader goroutine starting...")
	defer log.Println("FFmpeg output reader goroutine stopped.")
	defer close(rawFrameBuffer) // Signal downstream that no more raw frames will come

	frameBufferBytes := make([]byte, frameSizeBytes)
	for {
		n, err := io.ReadFull(ffmpegOutputReader, frameBufferBytes)
		if err != nil {
			if err == io.EOF || err == io.ErrClosedPipe {
				log.Println("FFmpeg output stream EOF or pipe closed.")
			} else {
				log.Printf("Error reading frame from FFmpeg output: %v (read %d bytes)", err, n)
			}
			return // Exit if pipe closes or error occurs
		}

		if n != frameSizeBytes {
			log.Printf("Warning: Read %d bytes from FFmpeg, expected %d. Skipping frame.", n, frameSizeBytes)
			continue
		}

		frameDataCopy := make([]byte, frameSizeBytes)
		copy(frameDataCopy, frameBufferBytes)

		select {
		case rawFrameBuffer <- frameDataCopy:
			// Frame successfully sent
		default:
			// log.Println("Warning: rawFrameBuffer channel full, dropping raw FFmpeg frame.")
		}
	}
}

// processRawFramesToImage converts raw byte slices from rawFrameBuffer into image.Image objects
// and sends them to the frameImageData channel.
func processRawFramesToImage(rawFrameBuffer chan []byte, frameImageData chan image.Image) {
	log.Println("Raw frame to image processor goroutine starting...")
	defer log.Println("Raw frame to image processor goroutine stopped.")
	// Do not close frameImageData here; its lifecycle is tied to the consumer (drawFrames)
	// or a higher-level cancellation mechanism.

	for rawFrameData := range rawFrameBuffer { // Loop until rawFrameBuffer is closed
		if len(rawFrameData) != frameSizeBytes {
			log.Printf("Warning: Received raw frame data of unexpected size: %d (expected %d) in processor", len(rawFrameData), frameSizeBytes)
			continue
		}

		img := &image.RGBA{
			Pix:    rawFrameData,
			Stride: videoWidth * bytesPerPixel,
			Rect:   image.Rect(0, 0, videoWidth, videoHeight),
		}

		select {
		case frameImageData <- img:
			// Image successfully sent
		default:
			// log.Println("Warning: frameImageData channel full, dropping processed image.")
		}
	}
}

// drawFrames receives processed images from frameImageChan, draws them onto the imageCanvas,
// and updates an FPS counter based on actual time elapsed.
func drawFrames(imageCanvas *canvas.Image, frameImageChan chan image.Image, fpsDisplayLabel *widget.Label) {
	log.Println("Frame drawing goroutine starting...")
	defer log.Println("Frame drawing goroutine stopped.")

	var frameCountSinceLastFPSCalc int64
	var lastImageRendered image.Image
	lastFPSTime := time.Now() // Time of the last FPS calculation

	// Ticker for UI refresh rate (how often we attempt to draw to canvas)
	uiRefreshTicker := time.NewTicker(16 * time.Millisecond) // Approx 60Hz
	defer uiRefreshTicker.Stop()

	// Ticker for how often we update the FPS display label
	fpsDisplayUpdateTicker := time.NewTicker(1 * time.Second)
	defer fpsDisplayUpdateTicker.Stop()

	for {
		select {
		case img, ok := <-frameImageChan:
			if !ok { // frameImageChan was closed
				log.Println("frameImageChan closed. Exiting drawFrames.")
				if fpsDisplayLabel != nil {
					fpsDisplayLabel.SetText("FPS: N/A (Stream Ended)")
				}
				return
			}
			lastImageRendered = img      // Store the latest image received
			frameCountSinceLastFPSCalc++ // Count this frame for the current FPS calculation period

		case <-uiRefreshTicker.C:
			// Time to refresh the UI. Draw the latest available image.
			if lastImageRendered != nil && imageCanvas != nil {
				imageCanvas.Image = lastImageRendered
				imageCanvas.Refresh()
			}

		case <-fpsDisplayUpdateTicker.C:
			// Time to calculate and update the FPS display.
			now := time.Now()
			elapsedSeconds := now.Sub(lastFPSTime).Seconds()

			if fpsDisplayLabel != nil {
				if elapsedSeconds > 0 { // Avoid division by zero if ticker fires too fast or time hasn't advanced
					fps := float64(frameCountSinceLastFPSCalc) / elapsedSeconds
					fpsDisplayLabel.SetText(fmt.Sprintf("FPS: %.1f", fps))
				} else {
					// If no time elapsed, show the raw count (should be rare with 1s ticker)
					fpsDisplayLabel.SetText(fmt.Sprintf("FPS: %d (inst)", frameCountSinceLastFPSCalc))
				}
			}
			frameCountSinceLastFPSCalc = 0 // Reset counter for the next calculation period
			lastFPSTime = now              // Update the time of the last FPS calculation
		}
	}
}

package main

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"log"
	"time"

	"fyne.io/fyne/v2/canvas"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// Global channels for video processing pipeline, initialized here for clarity
var (
	// frameImageData channel passes processed RGBA images to the drawing goroutine.
	// Buffer size can be tuned based on performance.
	frameImageData = make(chan image.Image, 30) // Increased buffer

	// rawFrameBuffer channel passes raw byte slices (frames from FFmpeg) to image conversion goroutines.
	// Buffer size can be tuned.
	rawFrameBuffer = make(chan []byte, 10) // Increased buffer
)

const (
	videoWidth     = 1920
	videoHeight    = 1080
	bytesPerPixel  = 4 // RGBA
	frameSizeBytes = videoWidth * videoHeight * bytesPerPixel
)

// runFFmpegProcess starts and manages the FFmpeg process for video decoding.
// It reads from ffmpegInputReader (which gets data from the network stream)
// and writes decoded raw video frames to ffmpegOutputWriter.
func runFFmpegProcess(ffmpegInputReader *io.PipeReader, ffmpegOutputWriter *io.PipeWriter) {
	log.Println("FFmpeg process starting...")
	defer log.Println("FFmpeg process stopped.")
	defer ffmpegInputReader.Close() // Ensure pipes are closed to terminate dependent goroutines
	defer ffmpegOutputWriter.Close()

	// Continuously try to run FFmpeg in case it crashes
	for {
		stderr := &bytes.Buffer{}
		// Configure FFmpeg input and output.
		// Input from pipe:0 (ffmpegInputReader)
		// Output to pipe:1 (ffmpegOutputWriter) as raw RGBA video.
		err := ffmpeg.Input("pipe:0", ffmpeg.KwArgs{
			// Input options: These are crucial for low latency and handling potentially corrupt streams.
			"format":             "mpegts",                  // Assuming input is MPEG-TS from server
			"flags":              "low_delay",               // Prioritize low delay
			"fflags":             "nobuffer+discardcorrupt", // Don't buffer, discard corrupt units
			"protocol_whitelist": "pipe",                    // Allow reading from pipe
			"probesize":          "32K",                     // Smaller probesize for faster start, adjust if needed
			"analyzeduration":    "500000",                  // Shorter analyze duration (0.5s), adjust if needed
			// "hwaccel":            "auto",   // Or specific like "d3d11va", "cuda", "videotoolbox" - depends on OS/hardware
		}).
			Output("pipe:1", ffmpeg.KwArgs{
				// Output options:
				"format":    "rawvideo",                                    // Output raw video frames
				"pix_fmt":   "rgba",                                        // Pixel format for Fyne canvas
				"s":         fmt.Sprintf("%dx%d", videoWidth, videoHeight), // Output size
				"flags":     "low_delay",                                   // Prioritize low delay for output
				"fflags":    "+nobuffer",                                   // Don't buffer output
				"avioflags": "direct",                                      // Minimize buffering in AVIO context
			}).
			OverWriteOutput(). // Overwrite output if it exists (not typical for pipes)
			WithInput(ffmpegInputReader).
			WithOutput(ffmpegOutputWriter).
			WithErrorOutput(stderr). // Capture FFmpeg's stderr for debugging
			Run()

		if err != nil {
			log.Printf("FFmpeg process error: %v\nFFmpeg stderr: %s", err, stderr.String())
			// If FFmpeg exits, the pipes will likely close or cause errors in connected goroutines.
			// Consider if a restart delay or max restart attempts are needed.
			time.Sleep(2 * time.Second) // Wait before restarting
			log.Println("Attempting to restart FFmpeg process...")
			// Pipes need to be re-established if we want to truly restart.
			// For simplicity here, this loop might not fully recover if pipes are permanently broken.
			// A more robust solution would involve recreating pipes and restarting dependent goroutines.
			// However, if input pipe closes due to network issues, ffmpeg will exit, and this loop will retry.
			// If output pipe closes, it implies downstream processing stopped.
			return // Exit this goroutine if FFmpeg cannot run; main might need to handle this.
		}
		log.Println("FFmpeg process exited cleanly (should not happen with continuous stream). Restarting.")
		time.Sleep(1 * time.Second) // Brief pause before restarting
	}
}

// readFFmpegOutputToBuffer reads raw video frames from FFmpeg's output pipe
// and sends them as byte slices to the rawFrameBuffer channel for further processing.
func readFFmpegOutputToBuffer(ffmpegOutputReader *io.PipeReader, rawFrameBuffer chan []byte) {
	log.Println("FFmpeg output reader goroutine starting...")
	defer log.Println("FFmpeg output reader goroutine stopped.")
	defer close(rawFrameBuffer) // Close channel to signal downstream processors

	frameBuffer := make([]byte, frameSizeBytes)
	for {
		n, err := io.ReadFull(ffmpegOutputReader, frameBuffer)
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
			continue // Skip incomplete frame
		}

		// Create a copy of the frame data to send over the channel.
		// This is important because frameBuffer will be reused.
		frameDataCopy := make([]byte, frameSizeBytes)
		copy(frameDataCopy, frameBuffer)

		select {
		case rawFrameBuffer <- frameDataCopy:
			// Frame successfully sent to processing
		default:
			// log.Println("Warning: rawFrameBuffer channel full, dropping raw FFmpeg frame.") // Can be very verbose
		}
	}
}

// processRawFramesToImage converts raw byte slices from rawFrameBuffer into image.Image objects
// and sends them to the frameImageData channel for rendering.
// This can be run in multiple goroutines for parallel processing if needed.
func processRawFramesToImage(rawFrameBuffer chan []byte, frameImageData chan image.Image) {
	log.Println("Raw frame to image processor goroutine starting...")
	defer log.Println("Raw frame to image processor goroutine stopped.")
	// Do not close frameImageData here, as multiple processors might feed into it,
	// or it's closed by the main drawing loop's lifecycle.
	// For this setup, drawFrames will consume until frameImageData is closed by a higher authority or never.

	for rawFrameData := range rawFrameBuffer { // Loop until rawFrameBuffer is closed
		if len(rawFrameData) != frameSizeBytes {
			log.Printf("Warning: Received raw frame data of unexpected size: %d (expected %d) in processor", len(rawFrameData), frameSizeBytes)
			continue // Skip this frame
		}

		// Create an image.RGBA pointing to the rawFrameData.
		// Note: If rawFrameData is reused by the sender, this could be problematic.
		// However, readFFmpegOutputToBuffer sends copies.
		img := &image.RGBA{
			Pix:    rawFrameData, // Use the received slice directly
			Stride: videoWidth * bytesPerPixel,
			Rect:   image.Rect(0, 0, videoWidth, videoHeight),
		}

		select {
		case frameImageData <- img:
			// Image successfully sent for drawing
		default:
			// log.Println("Warning: frameImageData channel full, dropping processed image.") // Can be very verbose
		}
	}
}

// drawFrames receives processed images from frameImageChan and draws them onto the imageCanvas.
func drawFrames(imageCanvas *canvas.Image, frameImageChan chan image.Image) {
	log.Println("Frame drawing goroutine starting...")
	defer log.Println("Frame drawing goroutine stopped.")

	ticker := time.NewTicker(16 * time.Millisecond) // Aim for ~60 FPS updates
	defer ticker.Stop()

	var lastImage image.Image

	for {
		select {
		case img, ok := <-frameImageChan:
			if !ok {
				log.Println("frameImageChan closed. Exiting drawFrames.")
				return // Channel closed, stop drawing
			}
			lastImage = img
			// Refresh is done by the ticker to control draw rate
		case <-ticker.C:
			if lastImage != nil && imageCanvas != nil {
				imageCanvas.Image = lastImage
				imageCanvas.Refresh()
				lastImage = nil // Avoid re-drawing the same image if no new one arrived
			}
		}
	}
}

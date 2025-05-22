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

var (
	frameImageData = make(chan image.Image, 30)
	rawFrameBuffer = make(chan []byte, 10)
)

const (
	videoWidth     = 1920
	videoHeight    = 1080
	bytesPerPixel  = 4
	frameSizeBytes = videoWidth * videoHeight * bytesPerPixel
)

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

			return
		}
		log.Println("FFmpeg process exited cleanly (unexpected for a continuous stream). Restarting.")
		time.Sleep(1 * time.Second)
	}
}

func readFFmpegOutputToBuffer(ffmpegOutputReader *io.PipeReader, rawFrameBuffer chan []byte) {
	log.Println("FFmpeg output reader goroutine starting...")
	defer log.Println("FFmpeg output reader goroutine stopped.")
	defer close(rawFrameBuffer)

	frameBufferBytes := make([]byte, frameSizeBytes)
	for {
		n, err := io.ReadFull(ffmpegOutputReader, frameBufferBytes)
		if err != nil {
			if err == io.EOF || err == io.ErrClosedPipe {
				log.Println("FFmpeg output stream EOF or pipe closed.")
			} else {
				log.Printf("Error reading frame from FFmpeg output: %v (read %d bytes)", err, n)
			}
			return
		}

		if n != frameSizeBytes {
			log.Printf("Warning: Read %d bytes from FFmpeg, expected %d. Skipping frame.", n, frameSizeBytes)
			continue
		}

		frameDataCopy := make([]byte, frameSizeBytes)
		copy(frameDataCopy, frameBufferBytes)

		select {
		case rawFrameBuffer <- frameDataCopy:

		default:

		}
	}
}

func processRawFramesToImage(rawFrameBuffer chan []byte, frameImageData chan image.Image) {
	log.Println("Raw frame to image processor goroutine starting...")
	defer log.Println("Raw frame to image processor goroutine stopped.")

	for rawFrameData := range rawFrameBuffer {
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

		default:

		}
	}
}

func drawFrames(imageCanvas *canvas.Image, frameImageChan chan image.Image, fpsDisplayLabel *widget.Label) {
	log.Println("Frame drawing goroutine starting...")
	defer log.Println("Frame drawing goroutine stopped.")

	var frameCountSinceLastFPSCalc int64
	var lastImageRendered image.Image
	lastFPSTime := time.Now()

	uiRefreshTicker := time.NewTicker(16 * time.Millisecond)
	defer uiRefreshTicker.Stop()

	fpsDisplayUpdateTicker := time.NewTicker(1 * time.Second)
	defer fpsDisplayUpdateTicker.Stop()

	for {
		select {
		case img, ok := <-frameImageChan:
			if !ok {
				log.Println("frameImageChan closed. Exiting drawFrames.")
				if fpsDisplayLabel != nil {
					fpsDisplayLabel.SetText("FPS: N/A (Stream Ended)")
				}
				return
			}
			lastImageRendered = img
			frameCountSinceLastFPSCalc++

		case <-uiRefreshTicker.C:

			if lastImageRendered != nil && imageCanvas != nil {
				imageCanvas.Image = lastImageRendered
				imageCanvas.Refresh()
			}

		case <-fpsDisplayUpdateTicker.C:

			now := time.Now()
			elapsedSeconds := now.Sub(lastFPSTime).Seconds()

			if fpsDisplayLabel != nil {
				if elapsedSeconds > 0 {
					fps := float64(frameCountSinceLastFPSCalc) / elapsedSeconds
					fpsDisplayLabel.SetText(fmt.Sprintf("FPS: %.1f", fps))
				} else {

					fpsDisplayLabel.SetText(fmt.Sprintf("FPS: %d (inst)", frameCountSinceLastFPSCalc))
				}
			}
			frameCountSinceLastFPSCalc = 0
			lastFPSTime = now
		}
	}
}

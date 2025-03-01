package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
	"image"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "control_grpc/gen/proto"

	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// mouseOverlay – прозрачный виджет, который реализует desktop.Hoverable.
// Он содержит ссылку на gRPC‑стрим и отправляет события мыши серверу.
type mouseOverlay struct {
	widget.BaseWidget
	stream pb.RemoteControlService_GetFeedClient
}

var mouseEvents = make(chan *pb.FeedRequest, 120)

// newMouseOverlay создаёт новый экземпляр mouseOverlay с привязанным gRPC‑стримом.
func newMouseOverlay(stream pb.RemoteControlService_GetFeedClient) *mouseOverlay {
	mo := &mouseOverlay{stream: stream}
	mo.ExtendBaseWidget(mo)
	return mo
}

// CreateRenderer возвращает пустой рендерер, так как виджет прозрачный.
func (mo *mouseOverlay) CreateRenderer() fyne.WidgetRenderer {
	empty := container.NewWithoutLayout()
	return widget.NewSimpleRenderer(empty)
}

// scaleCoordinates масштабирует координаты из текущего размера виджета в систему 1920×1080.
func (mo *mouseOverlay) scaleCoordinates(pos fyne.Position) (float32, float32) {
	sz := mo.Size()
	if sz.Width == 0 || sz.Height == 0 {
		return 0, 0
	}
	scaleX := 1920.0 / sz.Width
	scaleY := 1080.0 / sz.Height
	return pos.X * scaleX, pos.Y * scaleY
}

// sendMouseEvent формирует и отправляет сообщение о событии мыши через gRPC‑стрим.
func (mo *mouseOverlay) sendMouseEvent(eventType, btn string, pos fyne.Position) {
	sx, sy := mo.scaleCoordinates(pos)
	log.Printf("Mouse Event: %s, X: %d, Y: %d", eventType, int(sx), int(sy))
	req := &pb.FeedRequest{
		Message:        "mouse_event",
		MouseX:         int32(sx),
		MouseY:         int32(sy),
		MouseBtn:       btn,
		MouseEventType: eventType,
		ClientWidth:    1920,
		ClientHeight:   1080,
		Timestamp:      time.Now().UnixNano(),
	}

	// Отправка в канал вместо блокирующего stream.Send()
	select {
	case mouseEvents <- req:
		log.Println("Mouse event sent:", req)
	default:
		log.Println("Mouse event dropped (channel full)")
	}
}

// MouseIn вызывается, когда курсор входит в область виджета.
func (mo *mouseOverlay) MouseIn(ev *desktop.MouseEvent) {
	mo.sendMouseEvent("in", "", ev.Position)
}

// MouseMoved вызывается при перемещении курсора над виджетом.
func (mo *mouseOverlay) MouseMoved(ev *desktop.MouseEvent) {
	mo.sendMouseEvent("move", "", ev.Position)
}

// MouseOut вызывается, когда курсор покидает область виджета.
func (mo *mouseOverlay) MouseOut() {
	// Можно отправить событие "out" с нулевыми координатами
	req := &pb.FeedRequest{
		Message:        "mouse_event",
		MouseX:         0,
		MouseY:         0,
		MouseBtn:       "",
		MouseEventType: "out",
		ClientWidth:    1920,
		ClientHeight:   1080,
		Timestamp:      time.Now().UnixNano(),
	}
	if err := mo.stream.Send(req); err != nil {
		log.Printf("Ошибка отправки MouseOut: %v", err)
	}
}

type MouseTracker struct {
	mu       sync.Mutex
	mouseBtn string
}

func (mt *MouseTracker) setButton(btn string) {
	mt.mu.Lock()
	mt.mouseBtn = btn
	mt.mu.Unlock()
}

func (mt *MouseTracker) getButton() string {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	return mt.mouseBtn
}

func main() {
	a := app.New()
	w := a.NewWindow("Control GRPC client")

	// Определяем размеры для 720p и fullscreen (1920x1080)
	normalSize := fyne.NewSize(1280, 720)
	fullSize := fyne.NewSize(1920, 1080)

	// Создаем canvas для видео
	imageCanvas := canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 1920, 1080)))
	imageCanvas.SetMinSize(normalSize)

	// Настраиваем gRPC соединение
	tlsCredentials, err := loadTLSCredentials()
	if err != nil {
		log.Fatal("Cannot load TLS credentials: ", err)
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(tlsCredentials)}
	serverAddr := "localhost:32212"
	conn, err := grpc.NewClient(serverAddr, opts...)
	if err != nil {
		log.Fatal("Could not connect to server: ", err)
	}
	defer conn.Close()

	client := pb.NewRemoteControlServiceClient(conn)
	// Получаем двунаправленный поток
	stream, err := client.GetFeed(context.Background())
	if err != nil {
		log.Fatalf("Error creating stream: %v", err)
	}

	initRequest := &pb.FeedRequest{
		Message:      "init",
		MouseX:       0,
		MouseY:       0,
		ClientWidth:  1920,
		ClientHeight: 1080,
		Timestamp:    time.Now().UnixNano(),
	}
	if err := stream.Send(initRequest); err != nil {
		log.Fatalf("Ошибка отправки инициализации: %v", err)
	}

	// Создаем прозрачный оверлей, передавая ему ссылку на поток
	overlay := newMouseOverlay(stream)

	// Оборачиваем imageCanvas и overlay в контейнер, чтобы overlay располагался поверх изображения.
	videoContainer := container.NewStack(imageCanvas, overlay)

	// Горутина для обработки отправки координат
	go func() {
		for req := range mouseEvents {
			if err := stream.Send(req); err != nil {
				log.Printf("Ошибка отправки mouse event: %v", err)
			}
		}
	}()

	// Создаем метку и кнопку для переключения полноэкранного режима.
	widgetLabel := widget.NewLabel("Video Feed:")
	toggleButtonText := binding.NewString()
	toggleButtonText.Set("Full screen")
	isFull := false
	toggleButton := widget.NewButton("", func() {
		if !isFull {
			w.SetFullScreen(true)
			imageCanvas.SetMinSize(fullSize)
			isFull = true
			toggleButtonText.Set("Ne full screen")
		} else {
			w.SetFullScreen(false)
			imageCanvas.SetMinSize(normalSize)
			isFull = false
			toggleButtonText.Set("Full screen")
		}
		w.Content().Refresh()
	})
	toggleButtonText.AddListener(binding.NewDataListener(func() {
		text, _ := toggleButtonText.Get()
		toggleButton.SetText(text)
	}))

	// Собираем верхнюю панель и основной контент.
	topBar := container.NewHBox(widgetLabel, toggleButton)
	content := container.NewBorder(topBar, nil, nil, nil, videoContainer)
	w.SetContent(content)

	ffmpegInputReader, ffmpegInputWriter := io.Pipe()
	ffmpegOutputReader, ffmpegOutputWriter := io.Pipe()

	go func() {
		stderr := &bytes.Buffer{}
		err := ffmpeg.Input("pipe:0", ffmpeg.KwArgs{
			"format":             "mpegts",
			"flags":              "low_delay",
			"fflags":             "nobuffer+discardcorrupt",
			"protocol_whitelist": "pipe",
			"probesize":          "32",
			"analyzeduration":    "0",
			"hwaccel":            "d3d11va",
		}).
			Output("pipe:1", ffmpeg.KwArgs{
				"format":    "rawvideo",
				"pix_fmt":   "rgba",
				"s":         "1920x1080",
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
			log.Printf("FFmpeg error: %v\nOutput: %s", err, stderr.String())
			time.Sleep(time.Second)
			main()
		}
	}()

	frameImageData := make(chan image.Image, 30)
	rawFrameBuffer := make(chan []byte, 5)

	for i := 0; i < 3; i++ {
		go func() {
			frameSize := 1920 * 1080 * 4
			buf := make([]byte, frameSize)
			for {
				select {
				case data := <-rawFrameBuffer:
					copy(buf, data)
					img := &image.RGBA{
						Pix:    buf,
						Stride: 1920 * 4,
						Rect:   image.Rect(0, 0, 1920, 1080),
					}
					frameImageData <- img
				}
			}
		}()
	}

	go func() {
		frameSize := 1920 * 1080 * 4
		buf := make([]byte, frameSize)
		for {
			_, err := io.ReadFull(ffmpegOutputReader, buf)
			if err != nil {
				if err == io.EOF {
					return
				}
				log.Println("Error reading frame:", err)
				continue
			}
			copyBuf := make([]byte, frameSize)
			copy(copyBuf, buf)
			rawFrameBuffer <- copyBuf
		}
	}()

	go forwardVideoFeed(stream, ffmpegInputWriter, frameImageData)
	go drawFrames(imageCanvas, frameImageData)
	// Дополнительно можно оставить вызов trackAndPollMouse для обработки системных событий мыши
	// go trackAndPollMouse(stream)

	w.ShowAndRun()
}

func drawFrames(imageCanvas *canvas.Image, frameImageChan chan image.Image) {
	for img := range frameImageChan {
		imageCanvas.Image = img
		imageCanvas.Refresh()
	}
}

func loadTLSCredentials() (credentials.TransportCredentials, error) {
	clientCert, err := tls.LoadX509KeyPair("client.crt", "client.key")
	if err != nil {
		return nil, err
	}
	serverCACert, err := os.ReadFile("server.crt")
	if err != nil {
		return nil, err
	}
	serverCertPool := x509.NewCertPool()
	if !serverCertPool.AppendCertsFromPEM(serverCACert) {
		return nil, err
	}
	config := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      serverCertPool,
	}
	return credentials.NewTLS(config), nil
}

func forwardVideoFeed(stream pb.RemoteControlService_GetFeedClient, ffmpegInput io.Writer, frameImageData chan image.Image) {
	defer close(frameImageData)
	for {
		frame, err := stream.Recv()
		if err != nil {
			return
		}
		if err != nil {
			log.Printf("Failed to receive feed: %v", err)
			return
		}
		if _, err := ffmpegInput.Write(frame.Data); err != nil {
			log.Printf("Error writing to FFmpeg: %v", err)
			return
		}
	}
}

package main

import (
	"io"
	"log"
	"strings"
	"time"

	"github.com/go-vgo/robotgo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "control_grpc/gen/proto"
	"control_grpc/server/screen"
)

func (s *server) GetFeed(stream pb.RemoteControlService_GetFeedServer) error {
	serverWidth, serverHeight := robotgo.GetScreenSize()
	log.Printf("Server screen dimensions: %dx%d", serverWidth, serverHeight)

	capture, err := screen.NewScreenCapture()
	if err != nil {
		log.Printf("Error initializing screen capture: %v", err)
		return status.Errorf(codes.Internal, "Failed to initialize screen capture: %v", err)
	}
	defer capture.Close()

	reqMsgInit, err := stream.Recv()
	if err != nil {
		if err == io.EOF {
			log.Println("Client closed stream before init.")
			return nil
		}
		log.Printf("Failed to receive initial message: %v", err)
		return status.Errorf(codes.InvalidArgument, "Failed to receive initial message: %v", err)
	}
	log.Printf("Received init message from client: Width=%d, Height=%d", reqMsgInit.GetClientWidth(), reqMsgInit.GetClientHeight())

	scaleX, scaleY := getScaleFactors(serverWidth, serverHeight, reqMsgInit)
	log.Printf("Calculated scale factors: ScaleX=%.2f, ScaleY=%.2f", scaleX, scaleY)

	inputEvents := make(chan *pb.FeedRequest, 120)

	go handleInputEvents(inputEvents, scaleX, scaleY)
	go receiveInputEvents(stream, inputEvents)

	return sendScreenFeed(stream, capture)
}

func getScaleFactors(serverWidth, serverHeight int, reqMsgInit *pb.FeedRequest) (float32, float32) {
	if reqMsgInit.GetClientWidth() == 0 || reqMsgInit.GetClientHeight() == 0 {
		log.Println("Client width or height is zero, using 1.0 for scale factors.")
		return 1.0, 1.0
	}
	scaleX := float32(serverWidth) / float32(reqMsgInit.GetClientWidth())
	scaleY := float32(serverHeight) / float32(reqMsgInit.GetClientHeight())
	return scaleX, scaleY
}

func mapFyneKeyToRobotGo(fyneKeyName string) (key string, isSpecial bool) {
	switch fyneKeyName {
	case "Return", "Enter":
		return "enter", true
	case "Space":
		return "space", true
	case "Backspace":
		return "backspace", true
	case "Delete":
		return "delete", true
	case "Tab":
		return "tab", true
	case "Escape":
		return "escape", true
	case "Up":
		return "up", true
	case "Down":
		return "down", true
	case "Left":
		return "left", true
	case "Right":
		return "right", true
	case "Home":
		return "home", true
	case "End":
		return "end", true
	case "PageUp":
		return "pageup", true
	case "PageDown":
		return "pagedown", true
	case "ShiftL", "LeftShift", "ShiftR", "RightShift":
		return "shift", true
	case "ControlL", "LeftControl", "ControlR", "RightControl":
		return "ctrl", true
	case "AltL", "LeftAlt", "AltR", "RightAlt", "Menu":
		return "alt", true
	case "SuperL", "LeftSuper", "SuperR", "RightSuper", "MetaL", "MetaR":
		return "cmd", true
	case "F1":
		return "f1", true
	case "F2":
		return "f2", true
	case "F3":
		return "f3", true
	case "F4":
		return "f4", true
	case "F5":
		return "f5", true
	case "F6":
		return "f6", true
	case "F7":
		return "f7", true
	case "F8":
		return "f8", true
	case "F9":
		return "f9", true
	case "F10":
		return "f10", true
	case "F11":
		return "f11", true
	case "F12":
		return "f12", true
	case "Num0", "Num1", "Num2", "Num3", "Num4", "Num5", "Num6", "Num7", "Num8", "Num9":
		return strings.ToLower(strings.TrimPrefix(fyneKeyName, "Num")), true
	case "NumLock":
		return "numlock", true
	case "NumEnter":
		return "enter", true
	case "NumAdd", "NumpadAdd":
		return "+", true
	case "NumSubtract", "NumpadSubtract":
		return "-", true
	case "NumMultiply", "NumpadMultiply":
		return "*", true
	case "NumDivide", "NumpadDivide":
		return "/", true
	case "NumDecimal", "NumpadDecimal":
		return ".", true
	default:
		if strings.HasPrefix(fyneKeyName, "Key") && len(fyneKeyName) == 4 {
			char := fyneKeyName[3:]
			if len(char) == 1 && ((char[0] >= 'A' && char[0] <= 'Z') || (char[0] >= '0' && char[0] <= '9')) {
				return strings.ToLower(char), false
			}
		}
		if len(fyneKeyName) == 1 {
			return strings.ToLower(fyneKeyName), false
		}
		log.Printf("Unhandled Fyne key name for mapping: '%s'", fyneKeyName)
		return strings.ToLower(fyneKeyName), false
	}
}

func handleInputEvents(inputEvents chan *pb.FeedRequest, scaleX, scaleY float32) {
	log.Println("Input event handler goroutine started.")
	defer log.Println("Input event handler goroutine stopped.")

	for reqMsg := range inputEvents {
		switch reqMsg.Message {
		case "mouse_event":
			serverX := int(float32(reqMsg.GetMouseX()) * scaleX)
			serverY := int(float32(reqMsg.GetMouseY()) * scaleY)
			robotgo.Move(serverX, serverY)
			eventType := reqMsg.GetMouseEventType()
			mouseBtn := reqMsg.GetMouseBtn()

			if eventType == "down" {
				robotgo.MouseDown(mouseBtn)
			} else if eventType == "up" {
				robotgo.MouseUp(mouseBtn)
			}

		case "keyboard_event":
			kbEventType := reqMsg.GetKeyboardEventType()
			fyneKeyName := reqMsg.GetKeyName()
			keyChar := reqMsg.GetKeyCharStr()

			log.Printf("Received KeyboardEvent: Type='%s', FyneKeyName='%s', KeyChar='%s', Modifiers: Shift[%t], Ctrl[%t], Alt[%t], Super[%t]",
				kbEventType, fyneKeyName, keyChar, reqMsg.GetModifierShift(), reqMsg.GetModifierCtrl(), reqMsg.GetModifierAlt(), reqMsg.GetModifierSuper())

			robotgoKeyName, isSpecial := mapFyneKeyToRobotGo(fyneKeyName)
			if fyneKeyName != "" {
				log.Printf("Mapped FyneKeyName '%s' to robotgoKeyName '%s' (isSpecial: %t)", fyneKeyName, robotgoKeyName, isSpecial)
			}

			if kbEventType == "keydown" && robotgoKeyName == "delete" && reqMsg.GetModifierCtrl() && reqMsg.GetModifierAlt() {
				log.Println("Action: Simulating Ctrl+Alt+Delete")
				robotgo.KeyToggle("ctrl", "down")
				robotgo.KeyToggle("alt", "down")
				robotgo.KeyTap("delete")
				robotgo.KeyToggle("alt", "up")
				robotgo.KeyToggle("ctrl", "up")
			} else {
				switch kbEventType {
				case "keydown":
					if robotgoKeyName != "" {
						isModifierKey := robotgoKeyName == "shift" || robotgoKeyName == "ctrl" || robotgoKeyName == "alt" || robotgoKeyName == "cmd"
						if isModifierKey {
							log.Printf("Action: Modifier '%s' pressed down", robotgoKeyName)
							robotgo.KeyToggle(robotgoKeyName, "down")
						} else if isSpecial {
							log.Printf("Action: Tapping special key '%s'", robotgoKeyName)
							robotgo.KeyTap(robotgoKeyName)
						} else {
							log.Printf("Action: Tapping key '%s'", robotgoKeyName)
							robotgo.KeyTap(robotgoKeyName)
						}
					} else if keyChar != "" {
						log.Printf("Action: Typing character from keyChar on keydown '%s'", keyChar)
						robotgo.TypeStr(keyChar)
					} else {
						log.Printf("Action: Ignoring keydown event with empty robotgoKeyName and KeyChar.")
					}

				case "keyup":
					if robotgoKeyName != "" {
						isModifierKey := robotgoKeyName == "shift" || robotgoKeyName == "ctrl" || robotgoKeyName == "alt" || robotgoKeyName == "cmd"
						if isModifierKey {
							log.Printf("Action: Modifier '%s' released", robotgoKeyName)
							robotgo.KeyToggle(robotgoKeyName, "up")
						} else {
							// Non-modifier keyup events are generally ignored because KeyTap handles press & release.
							// Logging it for now to observe behavior.
							log.Printf("Action: Ignoring non-modifier keyup for '%s' (handled by KeyTap on keydown)", robotgoKeyName)
						}
					} else {
						log.Printf("Action: Ignoring keyup event with empty robotgoKeyName.")
					}

				case "keychar":
					if keyChar != "" {
						log.Printf("Action: Typing character from keychar event '%s'", keyChar)
						robotgo.TypeStr(keyChar)
					} else {
						log.Printf("Action: Ignoring keychar event with empty KeyChar.")
					}
				default:
					log.Printf("Action: Unhandled keyboard event type: '%s'", kbEventType)
				}
			}
		default:
			log.Printf("Unknown input event message type: %s", reqMsg.Message)
		}
	}
}

func receiveInputEvents(stream pb.RemoteControlService_GetFeedServer, inputEvents chan *pb.FeedRequest) {
	log.Println("Input event receiver goroutine started.")
	defer log.Println("Input event receiver goroutine stopped.")
	defer close(inputEvents)

	for {
		reqMsg, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				log.Println("Client closed the stream (EOF in receiveInputEvents).")
				return
			}
			s, ok := status.FromError(err)
			if ok && (s.Code() == codes.Canceled || s.Code() == codes.Unavailable) {
				log.Printf("Stream canceled or unavailable in receiveInputEvents: %v", err)
				return
			}
			log.Printf("Error receiving input event from stream: %v", err)
			return
		}

		select {
		case inputEvents <- reqMsg:

		default:
			log.Println("Input event channel full, dropping event.")
		}
	}
}

func sendScreenFeed(stream pb.RemoteControlService_GetFeedServer, capture *screen.ScreenCapture) error {
	log.Println("Screen feed sender goroutine started.")
	defer log.Println("Screen feed sender goroutine stopped.")

	frameBuffer := make([]byte, 2*1024*1024)
	ticker := time.NewTicker(time.Second / 30)
	defer ticker.Stop()

	var frameCounter int32 = 0
	for {
		select {
		case <-ticker.C:
			n, err := capture.ReadFrame(frameBuffer)
			if err != nil {
				if err == io.EOF {
					log.Println("Screen capture source reported EOF.")
					return status.Errorf(codes.Internal, "Screen capture source EOF")
				}
				log.Printf("Error reading frame from screen capture: %v", err)
				continue
			}
			if n == 0 {
				continue
			}

			err = stream.Send(&pb.FeedResponse{
				Data:        frameBuffer[:n],
				FrameNumber: frameCounter,
				Timestamp:   time.Now().UnixNano(),
				ContentType: "video/mp2t",
				HwAccel:     screen.Accel,
			})
			if err != nil {
				s, ok := status.FromError(err)
				if ok && (s.Code() == codes.Canceled || s.Code() == codes.Unavailable) {
					log.Printf("Client disconnected or stream unavailable during send: %v", err)
					return nil
				}
				log.Printf("Error sending frame to client: %v", err)
				return status.Errorf(codes.Internal, "Failed to send frame: %v", err)
			}
			frameCounter++
		case <-stream.Context().Done():
			log.Printf("Stream context done (client likely disconnected): %v", stream.Context().Err())
			return nil
		}
	}
}

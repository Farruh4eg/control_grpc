package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	pb "control_grpc/gen/proto"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	AppInstance     fyne.App
	filesClient     pb.FileTransferServiceClient
	mainWindow      fyne.Window
	refreshTreeChan = make(chan string, 1)
	treeDataMutex   sync.RWMutex
	nodesMap        = make(map[string]*pb.FSNode)
	childrenMap     = make(map[string][]string)
)

func InitializeSharedGlobals(theApp fyne.App, mainWin fyne.Window, client pb.FileTransferServiceClient) {
	AppInstance = theApp
	mainWindow = mainWin
	filesClient = client
	log.Println("INFO (file_system.go): Shared globals (AppInstance, mainWindow, filesClient) have been set.")
}

func createTreeChildrenFunc(id widget.TreeNodeID) []widget.TreeNodeID {
	treeDataMutex.RLock()
	children, ok := childrenMap[id]
	treeDataMutex.RUnlock()
	if ok {
		return children
	}
	return nil
}

func isTreeNodeBranchFunc(id widget.TreeNodeID) bool {
	if id == "" {
		return true
	}
	treeDataMutex.RLock()
	node, ok := nodesMap[id]
	treeDataMutex.RUnlock()
	return ok && node.Type == pb.FSNode_FOLDER
}

func createTreeNodeFunc(isBranch bool) fyne.CanvasObject {
	icon := widget.NewIcon(nil)
	label := widget.NewLabel("Template")
	downloadButton := widget.NewButtonWithIcon("", theme.DownloadIcon(), nil)
	downloadButton.Hide()
	return container.NewHBox(icon, label, downloadButton)
}

func updateTreeNodeFunc(id widget.TreeNodeID, isBranch bool, node fyne.CanvasObject) {
	if id == "" {
		return
	}

	treeDataMutex.RLock()
	fsNode, ok := nodesMap[id]
	treeDataMutex.RUnlock()

	hbox := node.(*fyne.Container)
	icon := hbox.Objects[0].(*widget.Icon)
	label := hbox.Objects[1].(*widget.Label)
	downloadButton := hbox.Objects[2].(*widget.Button)

	if ok {
		displayName := fsNode.Name
		if displayName == "" {
			displayName = filepath.Base(fsNode.Path)
		}
		if fsNode.Path == "/" {
			displayName = "/"
		} else if runtime.GOOS == "windows" && strings.HasSuffix(fsNode.Path, `:\`) {
			displayName = fsNode.Path
		}
		label.SetText(displayName)

		if fsNode.Type == pb.FSNode_FOLDER {
			icon.SetResource(theme.FolderIcon())
			downloadButton.Show()
			buttonNodeID := id
			downloadButton.OnTapped = func() {
				log.Printf("Folder download button clicked for: %s", buttonNodeID)
				treeDataMutex.RLock()
				nodeToDownload, nodeOk := nodesMap[buttonNodeID]
				treeDataMutex.RUnlock()
				if nodeOk && nodeToDownload.Type == pb.FSNode_FOLDER {
					log.Printf("Initiating folder download as zip for: %s", nodeToDownload.Path)
					if AppInstance == nil {
						log.Println("ERROR: AppInstance is nil in download button callback (updateTreeNodeFunc)!")
						if mainWindow != nil {
							dialog.ShowError(fmt.Errorf("Application instance not available for download window."), mainWindow)
						}
						return
					}
					go startDownload(AppInstance, nodeToDownload.Path, true)
				} else {
					log.Printf("Error: Download button tapped for non-folder or unknown node: %s", buttonNodeID)
				}
			}
		} else {
			if strings.HasPrefix(displayName, "[Error:") || strings.HasPrefix(displayName, "[Server Error:") {
				icon.SetResource(theme.ErrorIcon())
			} else {
				icon.SetResource(theme.FileIcon())
			}
			downloadButton.Hide()
			downloadButton.OnTapped = nil
		}
	} else {
		label.SetText("Error: Node data missing")
		icon.SetResource(theme.ErrorIcon())
		downloadButton.Hide()
		downloadButton.OnTapped = nil
		log.Printf("Error: Tree update - Node not found in nodesMap for ID: %s", id)
	}
}

func onTreeBranchOpened(id widget.TreeNodeID, fileTree *widget.Tree) {
	log.Printf("Tree branch opened: %s", id)
	treeDataMutex.RLock()
	_, childrenLoaded := childrenMap[id]
	nodeInfo, nodeExists := nodesMap[id]
	treeDataMutex.RUnlock()

	needsLoading := false
	if id == "" {
		if !childrenLoaded {
			needsLoading = true
		}
	} else if nodeExists && nodeInfo.Type == pb.FSNode_FOLDER {
		if !childrenLoaded {
			needsLoading = true
		}
	}

	if needsLoading {
		log.Printf("Node '%s' needs loading children.", id)
		go fetchChildren(id)
	} else {
		log.Printf("Node '%s' children already loaded or not a loadable branch.", id)
	}
}

func onTreeBranchClosed(id widget.TreeNodeID) {
	log.Printf("Tree branch closed: %s", id)
}

func onTreeNodeSelected(id string) {
	treeDataMutex.RLock()
	node, ok := nodesMap[id]
	treeDataMutex.RUnlock()

	if ok {
		log.Printf("Selected: %s (Path: %s, Type: %s)", id, node.Path, node.Type)
		if node.Type == pb.FSNode_FILE {
			displayName := node.Name
			if displayName == "" {
				displayName = filepath.Base(node.Path)
			}
			if !(strings.HasPrefix(displayName, "[Error:") || strings.HasPrefix(displayName, "[Server Error:")) {
				log.Printf("File selected: %s. Initiating download.", node.Path)
				if AppInstance == nil {
					log.Println("ERROR: AppInstance is nil in onTreeNodeSelected!")
					if mainWindow != nil {
						dialog.ShowError(fmt.Errorf("Application instance not available for download."), mainWindow)
					}
					return
				}
				go startDownload(AppInstance, node.Path, false)
			} else {
				log.Printf("Error node selected: %s. No download action.", node.Path)
			}
		}
	} else {
		log.Printf("Selected unknown node ID: %s", id)
	}
}

func fetchChildren(parentPath string) {
	log.Printf("Fetching children for path: '%s'", parentPath)
	if filesClient == nil {
		log.Println("Error: filesClient is nil in fetchChildren.")
		treeDataMutex.Lock()
		errorMsg := "[Error: Client not connected]"
		errorNodePath := filepath.Join(parentPath, errorMsg)
		nodesMap[errorNodePath] = &pb.FSNode{Path: errorNodePath, Name: errorMsg, Type: pb.FSNode_FILE}
		childrenMap[parentPath] = []string{errorNodePath}
		treeDataMutex.Unlock()
		select {
		case refreshTreeChan <- parentPath:
		default:
			log.Printf("Warning: Refresh channel full, could not signal for parent: '%s'", parentPath)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := &pb.FSRequest{Path: parentPath}
	res, err := filesClient.GetFS(ctx, req)

	treeDataMutex.Lock()
	defer treeDataMutex.Unlock()

	if err != nil {
		log.Printf("Error fetching children for '%s': %v", parentPath, err)
		s, _ := status.FromError(err)
		errorMsg := fmt.Sprintf("[Error %s: %s]", s.Code(), s.Message())
		errorNodePath := filepath.Join(parentPath, errorMsg)
		if len(errorNodePath) > 250 {
			errorNodePath = errorNodePath[:247] + "..."
		}
		nodesMap[errorNodePath] = &pb.FSNode{Path: errorNodePath, Name: filepath.Base(errorNodePath), Type: pb.FSNode_FILE}
		childrenMap[parentPath] = []string{errorNodePath}
	} else if res.ErrorMessage != "" {
		log.Printf("Server reported error for path '%s': %s", parentPath, res.ErrorMessage)
		errorMsg := fmt.Sprintf("[Server Error: %s]", res.ErrorMessage)
		errorNodePath := filepath.Join(parentPath, errorMsg)
		if len(errorNodePath) > 250 {
			errorNodePath = errorNodePath[:247] + "..."
		}
		nodesMap[errorNodePath] = &pb.FSNode{Path: errorNodePath, Name: filepath.Base(errorNodePath), Type: pb.FSNode_FILE}
		childrenMap[parentPath] = []string{errorNodePath}
	} else {
		log.Printf("Received %d children for path '%s'", len(res.Nodes), parentPath)
		childPaths := make([]string, 0, len(res.Nodes))
		childrenMap[parentPath] = []string{}

		for _, node := range res.Nodes {
			if node == nil || node.Path == "" {
				log.Printf("Warning: Received nil or empty node for parent '%s'. Skipping.", parentPath)
				continue
			}
			if node.Name == "" {
				node.Name = filepath.Base(node.Path)
			}
			nodesMap[node.Path] = node
			childPaths = append(childPaths, node.Path)
		}
		childrenMap[parentPath] = childPaths
		log.Printf("Updated childrenMap for '%s' with %d paths", parentPath, len(childPaths))
	}

	select {
	case refreshTreeChan <- parentPath:
		log.Printf("Sent refresh signal for parent: '%s'", parentPath)
	default:
		log.Printf("Warning: Refresh channel full, could not signal for parent: '%s'", parentPath)
	}
}

func startDownload(theApp fyne.App, remotePath string, isFolder bool) {
	log.Printf("Initiating download for remote path: '%s' (Is Folder: %v)", remotePath, isFolder)

	if filesClient == nil {
		log.Println("Error: filesClient is nil. Cannot start download.")
		if mainWindow != nil {
			dialog.ShowError(fmt.Errorf("file transfer client not initialized"), mainWindow)
		}
		return
	}
	if theApp == nil {
		log.Println("Error: Fyne app instance is nil in startDownload (should not happen if called correctly).")
		if mainWindow != nil {
			dialog.ShowError(fmt.Errorf("application instance not available"), mainWindow)
		}
		return
	}
	if mainWindow == nil {
		log.Println("Error: mainWindow is nil in startDownload. Cannot show save dialog.")
		var parentWinForDialog fyne.Window
		if drv := theApp.Driver(); drv != nil && len(drv.AllWindows()) > 0 {
			parentWinForDialog = drv.AllWindows()[0]
		}
		dialog.NewError(fmt.Errorf("main window not available for save dialog"), parentWinForDialog).Show()
		return
	}

	localFileName := filepath.Base(remotePath)
	if isFolder {
		localFileName += ".zip"
	}

	saveDialog := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			log.Printf("Error from file save dialog: %v", err)
			dialog.ShowError(fmt.Errorf("error selecting save location: %v", err), mainWindow)
			return
		}
		if writer == nil {
			log.Println("File save dialog cancelled by user.")
			return
		}
		localFilePath := writer.URI().Path()
		log.Printf("User selected local path: '%s' for remote '%s'", localFilePath, remotePath)
		go performDownload(theApp, remotePath, localFilePath, writer, isFolder)
	}, mainWindow)

	saveDialog.SetFileName(localFileName)
	saveDialog.Show()
}

func performDownload(theApp fyne.App, remotePath string, localFilePath string, writer fyne.URIWriteCloser, isFolder bool) {
	log.Printf("Performing download of '%s' (Folder: %v) to '%s'", remotePath, isFolder, localFilePath)
	defer writer.Close()

	if theApp == nil {
		log.Println("CRITICAL: Fyne app instance is nil in performDownload. Cannot create download window.")
		return
	}

	dlWindow := theApp.NewWindow(fmt.Sprintf("Downloading %s", filepath.Base(localFilePath)))

	statusLabel := widget.NewLabel(fmt.Sprintf("Starting download of %s...", filepath.Base(localFilePath)))
	progressBar := widget.NewProgressBar()
	progressBar.Min = 0
	progressBytesLabel := widget.NewLabel("0 B / 0 B")
	progressBytesLabel.Alignment = fyne.TextAlignCenter

	ctx, cancelFunc := context.WithCancel(context.Background())

	cancelButton := widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), func() {
		log.Printf("Download cancel button clicked for '%s'", remotePath)
		cancelFunc()
	})

	dlWindow.SetContent(container.NewVBox(
		statusLabel,
		progressBar,
		progressBytesLabel,
		cancelButton,
	))
	dlWindow.Resize(fyne.NewSize(400, 150))
	dlWindow.CenterOnScreen()

	dlWindow.SetCloseIntercept(func() {
		log.Printf("Download window close intercepted for '%s'. Cancelling download.", remotePath)
		cancelFunc()
	})

	dlWindow.Show()
	defer func() {
		log.Printf("Closing download window for %s", remotePath)
		dlWindow.Close()
	}()

	var streamClient interface {
		Recv() (*pb.FileChunk, error)
	}
	var streamErr error

	if isFolder {
		s, e := filesClient.DownloadFolderAsZip(ctx, &pb.FileRequest{Path: remotePath})
		streamClient = s
		streamErr = e
	} else {
		s, e := filesClient.DownloadFile(ctx, &pb.FileRequest{Path: remotePath})
		streamClient = s
		streamErr = e
	}

	if streamErr != nil {
		log.Printf("Error initiating download stream for '%s': %v", remotePath, streamErr)
		dlWindow.Hide()
		parentDialogWindow := mainWindow
		if status.Code(streamErr) == codes.Canceled {
			log.Printf("Download of '%s' was cancelled by user before stream start.", remotePath)
		} else {
			showDownloadError("Failed to start download", streamErr, parentDialogWindow)
		}
		return
	}

	totalBytesReceived := int64(0)
	var totalSize int64 = 0
	firstChunk := true

	for {
		select {
		case <-ctx.Done():
			log.Printf("Context cancelled during download of '%s'. Error: %v", remotePath, ctx.Err())
			return
		default:
		}

		chunk, err := streamClient.Recv()
		if err != nil {
			parentDialogWindow := mainWindow
			if err == io.EOF {
				log.Printf("Download of '%s' finished successfully. Total bytes: %d", remotePath, totalBytesReceived)

				if parentDialogWindow != nil {
					dialog.ShowInformation("Download Complete",
						fmt.Sprintf("Downloaded %s successfully to %s\n(%s received)",
							filepath.Base(localFilePath),
							localFilePath,
							formatBytes(totalBytesReceived)),
						parentDialogWindow)
				} else {
					log.Println("mainWindow is nil, cannot show download complete dialog.")
				}
			} else {
				log.Printf("Error receiving chunk for '%s': %v", remotePath, err)
				if status.Code(err) == codes.Canceled || ctx.Err() == context.Canceled {
					log.Printf("Download of '%s' was cancelled during streaming.", remotePath)
				} else {
					showDownloadError(fmt.Sprintf("Failed during download of %s", filepath.Base(localFilePath)), err, parentDialogWindow)
				}
			}
			return
		}

		if chunk.Content == nil {
			log.Printf("Received nil chunk content for '%s'. Skipping.", remotePath)
			continue
		}

		if firstChunk {
			if chunk.Metadata != nil && chunk.Metadata.TotalSize > 0 {
				totalSize = chunk.Metadata.TotalSize
				progressBar.Max = float64(totalSize)
				statusLabel.SetText(fmt.Sprintf("Downloading %s...", filepath.Base(localFilePath)))
			} else {
				statusLabel.SetText(fmt.Sprintf("Downloading %s (size unknown)...", filepath.Base(localFilePath)))
			}
			firstChunk = false
		}

		n, writeErr := writer.Write(chunk.Content)
		if writeErr != nil {
			log.Printf("Error writing chunk to local file '%s': %v", localFilePath, writeErr)
			parentDialogWindow := mainWindow
			showDownloadError(fmt.Sprintf("Error writing %s to disk", filepath.Base(localFilePath)), writeErr, parentDialogWindow)
			return
		}
		totalBytesReceived += int64(n)

		if totalSize > 0 {
			progressBar.SetValue(float64(totalBytesReceived))
			progressBytesLabel.SetText(fmt.Sprintf("%s / %s", formatBytes(totalBytesReceived), formatBytes(totalSize)))
		} else {
			progressBytesLabel.SetText(fmt.Sprintf("%s received", formatBytes(totalBytesReceived)))
		}
	}
}

func showDownloadError(title string, err error, parent fyne.Window) {
	if parent == nil {
		log.Printf("Error: parent window is nil in showDownloadError. Cannot show dialog. Title: %s, Error: %v", title, err)
		return
	}
	s, ok := status.FromError(err)
	var errMsg string
	if ok {
		errMsg = fmt.Sprintf("%s (gRPC Error %s: %s)", title, s.Code(), s.Message())
	} else {
		errMsg = fmt.Sprintf("%s: %v", title, err)
	}
	dialog.ShowError(fmt.Errorf(errMsg), parent)
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

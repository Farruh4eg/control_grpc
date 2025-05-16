package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	pb "control_grpc/gen/proto" // Assuming this path is correct
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// createTreeChildrenFunc is the callback for widget.Tree to get child nodes.
func createTreeChildrenFunc(id widget.TreeNodeID) []widget.TreeNodeID {
	treeDataMutex.RLock()
	children, ok := childrenMap[id]
	treeDataMutex.RUnlock()

	if id == "" {
		// log.Printf("Tree: getChildren for ROOT ('%s'). childrenMap[''] exists: %v, count: %d", id, ok, len(children))
	} else {
		// log.Printf("Tree: getChildren for '%s'. childrenMap['%s'] exists: %v, count: %d", id, id, ok, len(children))
	}

	if ok {
		return children // Children have been loaded previously
	}
	// Children not loaded yet. Loading is triggered by OnBranchOpened.
	return nil
}

// isTreeNodeBranchFunc is the callback for widget.Tree to check if a node is a branch.
func isTreeNodeBranchFunc(id widget.TreeNodeID) bool {
	if id == "" { // The root "" is always conceptually a branch.
		return true
	}
	treeDataMutex.RLock()
	node, ok := nodesMap[id]
	treeDataMutex.RUnlock()
	isBranch := ok && node.Type == pb.FSNode_FOLDER
	// log.Printf("Tree: isBranch check for '%s' -> %v (Node found: %v, Type Folder: %v)", id, isBranch, ok, ok && node.Type == pb.FSNode_FOLDER)
	return isBranch
}

// createTreeNodeFunc is the callback for widget.Tree to create a new node widget.
func createTreeNodeFunc(isBranch bool) fyne.CanvasObject {
	icon := widget.NewIcon(nil) // Placeholder icon
	label := widget.NewLabel("Template")
	downloadButton := widget.NewButtonWithIcon("", theme.DownloadIcon(), nil) // Action set in update
	downloadButton.Hide()                                                     // Hide by default

	// HBox places items side-by-side
	return container.NewHBox(icon, label, downloadButton)
}

// updateTreeNodeFunc is the callback for widget.Tree to update an existing node widget.
func updateTreeNodeFunc(id widget.TreeNodeID, isBranch bool, node fyne.CanvasObject) {
	if id == "" {
		// log.Printf("Tree: update called for root ID ('%s'). Skipping visual update for root itself.", id)
		return // Root "" doesn't have a visual representation to update directly in the tree.
	}

	treeDataMutex.RLock()
	fsNode, ok := nodesMap[id]
	treeDataMutex.RUnlock()

	hbox := node.(*fyne.Container) // Assuming HBox(icon, label, downloadButton)
	icon := hbox.Objects[0].(*widget.Icon)
	label := hbox.Objects[1].(*widget.Label)
	downloadButton := hbox.Objects[2].(*widget.Button)

	if ok {
		displayName := filepath.Base(fsNode.Path)
		if fsNode.Path == "/" {
			displayName = "/" // Posix root
		} else if runtime.GOOS == "windows" && strings.HasSuffix(fsNode.Path, `:\`) {
			displayName = fsNode.Path // Windows drive root (e.g., "C:\")
		} else if displayName == "" && fsNode.Type == pb.FSNode_FOLDER {
			displayName = fsNode.Path // Fallback for other cases
		} else if strings.HasPrefix(displayName, "[Error:") || strings.HasPrefix(displayName, "[Server Error:") {
			// If the base name is an error message, use it directly
		}

		label.SetText(displayName)

		if fsNode.Type == pb.FSNode_FOLDER {
			icon.SetResource(theme.FolderIcon())
			downloadButton.Show()
			buttonNodeID := id // Capture for closure
			downloadButton.OnTapped = func() {
				log.Printf("Folder download button clicked for: %s", buttonNodeID)
				treeDataMutex.RLock()
				nodeToDownload, nodeOk := nodesMap[buttonNodeID]
				treeDataMutex.RUnlock()
				if nodeOk && nodeToDownload.Type == pb.FSNode_FOLDER {
					log.Printf("Initiating folder download as zip for: %s", nodeToDownload.Path)
					// mainWindow should be accessible here if in the same package
					go startDownload(nodeToDownload.Path, true)
				} else {
					log.Printf("Error: Download button tapped for non-folder or unknown node: %s", buttonNodeID)
				}
			}
		} else { // FSNode_FILE or other (including error nodes displayed as files)
			// For error nodes, filepath.Base(fsNode.Path) will be the error message.
			// We can use a generic file icon or a specific error icon if desired.
			if strings.HasPrefix(displayName, "[Error:") || strings.HasPrefix(displayName, "[Server Error:") {
				icon.SetResource(theme.ErrorIcon()) // Use error icon for error nodes
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

// onTreeBranchOpened is called when a tree branch is opened by the user.
// It triggers fetching children for that branch if not already loaded.
func onTreeBranchOpened(id widget.TreeNodeID, fileTree *widget.Tree) {
	log.Printf("Tree branch opened: %s", id)

	treeDataMutex.RLock()
	_, childrenLoaded := childrenMap[id]
	nodeInfo, nodeExists := nodesMap[id] // nodeExists will be false for root ""
	treeDataMutex.RUnlock()

	needsLoading := false
	if id == "" { // Root node
		if !childrenLoaded {
			log.Printf("Root node ('%s') needs loading.", id)
			needsLoading = true
		} else {
			log.Printf("Root node ('%s') children already loaded.", id)
		}
	} else if nodeExists && nodeInfo.Type == pb.FSNode_FOLDER { // Non-root folder
		if !childrenLoaded {
			log.Printf("Node '%s' (folder) needs loading.", id)
			needsLoading = true
		} else {
			log.Printf("Node '%s' (folder) children already loaded.", id)
		}
	} else if !nodeExists && id != "" {
		log.Printf("Node '%s' does not exist in nodesMap. Cannot open.", id)
	} else if nodeInfo.Type != pb.FSNode_FOLDER { // Could be a file or an error node
		log.Printf("Node '%s' is not a folder type. Cannot open.", id)
	}

	if needsLoading {
		go fetchChildren(id) // Asynchronously fetch children
	}
}

// onTreeBranchClosed is called when a tree branch is closed.
func onTreeBranchClosed(id widget.TreeNodeID) {
	log.Printf("Tree branch closed: %s", id)
	// Optional: Clear childrenMap[id] to free memory and force reload on next open.
	// treeDataMutex.Lock()
	// delete(childrenMap, id)
	// treeDataMutex.Unlock()
}

// onTreeNodeSelected is called when a tree node is selected (single click).
// Triggers download for files.
func onTreeNodeSelected(id string) {
	treeDataMutex.RLock()
	node, ok := nodesMap[id]
	treeDataMutex.RUnlock()

	if ok {
		log.Printf("Selected: %s (Path: %s, Type: %s)", id, node.Path, node.Type)
		if node.Type == pb.FSNode_FILE {
			// Ensure it's not an error node before attempting download
			if !(strings.HasPrefix(filepath.Base(node.Path), "[Error:") || strings.HasPrefix(filepath.Base(node.Path), "[Server Error:")) {
				log.Printf("File selected: %s. Initiating download.", node.Path)
				// mainWindow should be accessible here
				go startDownload(node.Path, false)
			} else {
				log.Printf("Error node selected: %s. No download action.", node.Path)
			}
		} else {
			log.Printf("Folder selected: %s. No download action on single click (use download button or context menu).", node.Path)
		}
	} else {
		log.Printf("Selected unknown node ID: %s", id)
	}
}

// fetchChildren fetches child nodes for a given parent path from the server.
func fetchChildren(parentPath string) {
	log.Printf("Fetching children for path: '%s'", parentPath)
	if filesClient == nil {
		log.Println("Error: filesClient is nil in fetchChildren. Cannot fetch.")
		// Optionally, update UI to show this error for the specific parentPath
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := &pb.FSRequest{Path: parentPath}
	log.Printf("Attempting gRPC call GetFS for path: '%s'", parentPath)
	res, err := filesClient.GetFS(ctx, req)

	treeDataMutex.Lock() // Lock once before modifying shared maps
	if err != nil {
		log.Printf("Error fetching children for '%s': %v", parentPath, err)
		s, ok := status.FromError(err)
		errorMsg := ""
		if ok {
			errorMsg = fmt.Sprintf("[Error %s: %s]", s.Code(), s.Message())
		} else {
			errorMsg = fmt.Sprintf("[Error: %v]", err)
		}
		// Create an error node. The error message is part of the path.
		errorNodePath := filepath.Join(parentPath, errorMsg)
		if len(errorNodePath) > 250 { // Path segments can have limits
			errorNodePath = errorNodePath[:247] + "..."
		}
		// Ensure no field 'Name' is used here
		nodesMap[errorNodePath] = &pb.FSNode{Path: errorNodePath, Type: pb.FSNode_FILE} // Represent error as a file-like node
		childrenMap[parentPath] = []string{errorNodePath}                               // Show only the error node as child
	} else if res.ErrorMessage != "" {
		log.Printf("Server reported error for path '%s': %s", parentPath, res.ErrorMessage)
		errorMsg := fmt.Sprintf("[Server Error: %s]", res.ErrorMessage)
		errorNodePath := filepath.Join(parentPath, errorMsg)
		if len(errorNodePath) > 250 {
			errorNodePath = errorNodePath[:247] + "..."
		}
		// Ensure no field 'Name' is used here
		nodesMap[errorNodePath] = &pb.FSNode{Path: errorNodePath, Type: pb.FSNode_FILE}
		childrenMap[parentPath] = []string{errorNodePath}
	} else {
		log.Printf("Received %d children for path '%s'", len(res.Nodes), parentPath)
		childPaths := make([]string, 0, len(res.Nodes))
		childrenMap[parentPath] = []string{} // Clear previous children for this parent

		for _, node := range res.Nodes {
			if node == nil || node.Path == "" {
				log.Printf("Warning: Received nil or empty node for parent '%s'. Skipping.", parentPath)
				continue
			}
			nodesMap[node.Path] = node // Store/update node info
			childPaths = append(childPaths, node.Path)
		}
		childrenMap[parentPath] = childPaths
		log.Printf("Updated childrenMap for '%s' with %d paths", parentPath, len(childPaths))
	}
	treeDataMutex.Unlock() // Unlock after modifications

	// Signal the main thread to refresh the tree view for the parent
	select {
	case refreshTreeChan <- parentPath:
		log.Printf("Sent refresh signal for parent: '%s'", parentPath)
	default:
		log.Printf("Warning: Refresh channel full, could not signal for parent: '%s'", parentPath)
	}
}

// startDownload initiates the download process by showing a save dialog.
// mainWindow should be accessible here if all files are in `package main`.
func startDownload(remotePath string, isFolder bool) {
	log.Printf("Initiating download for remote path: '%s' (Is Folder: %v)", remotePath, isFolder)

	if filesClient == nil {
		log.Println("Error: filesClient is nil. Cannot start download.")
		if mainWindow != nil {
			dialog.ShowError(fmt.Errorf("file transfer client not initialized"), mainWindow)
		}
		return
	}

	localFileName := filepath.Base(remotePath)
	if isFolder {
		localFileName += ".zip" // Suggest .zip extension for folders
	}

	if mainWindow == nil {
		log.Println("Error: mainWindow is nil in startDownload. Cannot show save dialog.")
		return
	}

	saveDialog := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			log.Printf("Error from file save dialog: %v", err)
			dialog.ShowError(fmt.Errorf("error selecting save location: %v", err), mainWindow)
			return
		}
		if writer == nil {
			log.Println("File save dialog cancelled by user.")
			return // User cancelled
		}

		localFilePath := writer.URI().Path() // Get local path from URI
		log.Printf("User selected local path: '%s' for remote '%s'", localFilePath, remotePath)

		// Proceed with download in a new goroutine
		// Pass mainWindow to performDownload if it were not a package global
		go performDownload(remotePath, localFilePath, writer, isFolder)

	}, mainWindow)

	saveDialog.SetFileName(localFileName)
	saveDialog.Show()
}

// performDownload handles the gRPC streaming and file writing.
// mainWindow should be accessible here.
func performDownload(remotePath string, localFilePath string, writer fyne.URIWriteCloser, isFolder bool) {
	log.Printf("Performing download of '%s' (Folder: %v) to '%s'", remotePath, isFolder, localFilePath)
	defer writer.Close() // Ensure writer is closed on exit

	if mainWindow == nil {
		log.Println("Error: mainWindow is nil in performDownload. Cannot show progress/error dialogs.")
		// Attempt to proceed without dialogs, or return early
		// For now, we'll log and let it try, but dialogs won't appear.
	}

	var progressDialog dialog.Dialog
	if mainWindow != nil {
		progressLabel := widget.NewLabel(fmt.Sprintf("Downloading %s...", filepath.Base(localFilePath)))
		progressContent := container.NewVBox(progressLabel)
		progressDialog = dialog.NewCustom("Downloading", "Cancel", progressContent, mainWindow)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if progressDialog != nil {
		progressDialog.SetOnClosed(func() {
			log.Printf("Download dialog closed for '%s'. Cancelling gRPC context.", remotePath)
			cancel()
		})
		progressDialog.Show()
		defer progressDialog.Hide()
	}

	var streamClient interface {
		Recv() (*pb.FileChunk, error)
	}
	var streamErr error

	if isFolder {
		log.Printf("Calling DownloadFolderAsZip for remote path: '%s'", remotePath)
		s, e := filesClient.DownloadFolderAsZip(ctx, &pb.FileRequest{Path: remotePath})
		streamClient = s
		streamErr = e
	} else {
		log.Printf("Calling DownloadFile for remote path: '%s'", remotePath)
		s, e := filesClient.DownloadFile(ctx, &pb.FileRequest{Path: remotePath})
		streamClient = s
		streamErr = e
	}

	if streamErr != nil {
		log.Printf("Error initiating download stream for '%s': %v", remotePath, streamErr)
		if status.Code(streamErr) == codes.Canceled {
			log.Printf("Download of '%s' was cancelled by user before stream start.", remotePath)
			if mainWindow != nil { // Only show dialog if mainWindow is available
				// dialog.ShowInformation("Download Cancelled", "Download was cancelled.", mainWindow)
			}
		} else {
			showDownloadError(fmt.Sprintf("Failed to start download for %s", filepath.Base(localFilePath)), streamErr)
		}
		return
	}

	totalBytesReceived := int64(0)
	for {
		select {
		case <-ctx.Done():
			log.Printf("Context cancelled during download of '%s' (before Recv).", remotePath)
			if mainWindow != nil {
				dialog.ShowInformation("Download Cancelled", fmt.Sprintf("Download of %s was cancelled.", filepath.Base(localFilePath)), mainWindow)
			}
			return
		default:
		}

		chunk, err := streamClient.Recv()
		if err != nil {
			if err == io.EOF {
				log.Printf("Download of '%s' finished successfully. Total bytes: %d", remotePath, totalBytesReceived)
				if mainWindow != nil {
					dialog.ShowInformation("Download Complete", fmt.Sprintf("Downloaded %s successfully to %s", filepath.Base(localFilePath), localFilePath), mainWindow)
				}
				break
			}
			log.Printf("Error receiving chunk for '%s': %v", remotePath, err)
			if status.Code(err) == codes.Canceled || ctx.Err() == context.Canceled {
				log.Printf("Download of '%s' was cancelled during streaming.", remotePath)
				if mainWindow != nil {
					dialog.ShowInformation("Download Cancelled", fmt.Sprintf("Download of %s was cancelled.", filepath.Base(localFilePath)), mainWindow)
				}
			} else {
				showDownloadError(fmt.Sprintf("Failed during download of %s", filepath.Base(localFilePath)), err)
			}
			return
		}

		if chunk.Content == nil {
			log.Printf("Received nil chunk content for '%s'. Skipping.", remotePath)
			continue
		}

		n, writeErr := writer.Write(chunk.Content)
		if writeErr != nil {
			log.Printf("Error writing chunk to local file '%s': %v", localFilePath, writeErr)
			showDownloadError(fmt.Sprintf("Error writing %s to disk", filepath.Base(localFilePath)), writeErr)
			return
		}
		totalBytesReceived += int64(n)
	}
}

// showDownloadError is a helper to display download-related errors.
// mainWindow should be accessible here.
func showDownloadError(title string, err error) {
	if mainWindow == nil {
		log.Printf("Error: mainWindow is nil in showDownloadError. Cannot show dialog. Title: %s, Error: %v", title, err)
		return
	}
	s, ok := status.FromError(err)
	if ok {
		dialog.ShowError(fmt.Errorf("%s (gRPC Error %s: %s)", title, s.Code(), s.Message()), mainWindow)
	} else {
		dialog.ShowError(fmt.Errorf("%s: %v", title, err), mainWindow)
	}
}

package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"

	pb "control_grpc/gen/proto" // Assuming this path is correct
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetFS handles requests to list directory contents or filesystem roots.
func (s *server) GetFS(ctx context.Context, req *pb.FSRequest) (*pb.FSResponse, error) {
	reqPath := req.GetPath()
	log.Printf("GetFS request received for path: '%s'", reqPath)

	response := &pb.FSResponse{
		RequestedPath: reqPath,
		Nodes:         []*pb.FSNode{},
	}

	var nodes []*pb.FSNode
	var err error

	if reqPath == "" {
		// Requesting roots (e.g., drives on Windows, "/" on POSIX)
		log.Println("Requesting filesystem roots...")
		if runtime.GOOS == "windows" {
			nodes = getWindowsDrives()
		} else {
			nodes = getPosixRoot()
		}
	} else {
		// Requesting contents of a specific path
		log.Printf("Requesting contents of path: %s", reqPath)
		nodes, err = listDirectoryContents(reqPath)
		if err != nil {
			log.Printf("Error listing directory '%s': %v", reqPath, err)
			// Populate error message in response for client
			if os.IsPermission(err) {
				response.ErrorMessage = fmt.Sprintf("Permission denied: %v", filepath.Base(reqPath))
			} else if os.IsNotExist(err) {
				response.ErrorMessage = fmt.Sprintf("Path does not exist: %v", filepath.Base(reqPath))
			} else {
				response.ErrorMessage = fmt.Sprintf("Cannot access '%s': %v", filepath.Base(reqPath), err)
			}
		}
	}

	response.Nodes = nodes
	log.Printf("GetFS response for '%s': sending %d nodes, ErrorMessage: '%s'", reqPath, len(response.Nodes), response.ErrorMessage)
	return response, nil
}

// DownloadFile handles requests to stream a single file's content.
func (s *server) DownloadFile(req *pb.FileRequest, stream pb.FileTransferService_DownloadFileServer) error {
	filePath := req.GetPath()
	log.Printf("DownloadFile request received for path: '%s'", filePath)

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		log.Printf("Error stating file '%s': %v", filePath, err)
		if os.IsNotExist(err) {
			return status.Errorf(codes.NotFound, "File not found: %v", err)
		}
		return status.Errorf(codes.Internal, "Failed to access file information: %v", err)
	}
	if fileInfo.IsDir() {
		log.Printf("Attempted to download a directory using DownloadFile: '%s'", filePath)
		return status.Errorf(codes.InvalidArgument, "Path is a directory, not a file. Use DownloadFolderAsZip for directories.")
	}

	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("Error opening file '%s': %v", filePath, err)
		if os.IsPermission(err) {
			return status.Errorf(codes.PermissionDenied, "Permission denied to open file: %v", err)
		}
		return status.Errorf(codes.Internal, "Failed to open file: %v", err)
	}
	defer file.Close()

	buffer := make([]byte, 1024*64) // 64KB chunk size
	firstChunkSent := false
	log.Printf("Starting stream for file: '%s', Total size: %d bytes", filePath, fileInfo.Size())

	for {
		if err := stream.Context().Err(); err != nil {
			log.Printf("Client cancelled download of '%s': %v", filePath, err)
			return status.FromContextError(err).Err()
		}

		n, err := file.Read(buffer)
		if err != nil {
			if err == io.EOF {
				log.Printf("Finished streaming file: '%s'", filePath)
				break
			}
			log.Printf("Error reading file chunk for '%s': %v", filePath, err)
			return status.Errorf(codes.Internal, "Error reading file chunk: %v", err)
		}

		chunkToSend := &pb.FileChunk{Content: buffer[:n]}

		if !firstChunkSent {
			// Send metadata with the first chunk
			chunkToSend.Metadata = &pb.FileChunkMetadata{
				TotalSize: fileInfo.Size(),
			}
			log.Printf("Sending first chunk for '%s' with metadata (TotalSize: %d)", filePath, fileInfo.Size())
			firstChunkSent = true
		}

		sendErr := stream.Send(chunkToSend)
		if sendErr != nil {
			log.Printf("Error sending file chunk for '%s': %v", filePath, sendErr)
			if status.Code(sendErr) == codes.Canceled || status.Code(sendErr) == codes.Unavailable {
				log.Printf("Client cancelled or stream unavailable during send for '%s'", filePath)
				return sendErr
			}
			return status.Errorf(codes.Internal, "Error sending file chunk: %v", sendErr)
		}
	}
	log.Printf("Successfully streamed file: '%s'", filePath)
	return nil
}

// DownloadFolderAsZip handles requests to stream a folder's content as a zip archive.
func (s *server) DownloadFolderAsZip(req *pb.FileRequest, stream pb.FileTransferService_DownloadFolderAsZipServer) error {
	folderPath := req.GetPath()
	log.Printf("DownloadFolderAsZip request received for path: '%s'", folderPath)

	fileInfo, err := os.Stat(folderPath)
	if err != nil {
		log.Printf("Error stating folder '%s': %v", folderPath, err)
		if os.IsNotExist(err) {
			return status.Errorf(codes.NotFound, "Folder not found: %v", err)
		}
		return status.Errorf(codes.Internal, "Failed to access folder information: %v", err)
	}
	if !fileInfo.IsDir() {
		log.Printf("Path is not a directory: '%s'", folderPath)
		return status.Errorf(codes.InvalidArgument, "Path is not a directory.")
	}

	pipeReader, pipeWriter := io.Pipe()
	firstChunkSent := false // To send metadata with the first data chunk

	go func() {
		defer pipeWriter.Close()
		zipWriter := zip.NewWriter(pipeWriter)
		defer zipWriter.Close()

		log.Printf("Starting zipping process for folder: '%s'", folderPath)
		walkErr := filepath.Walk(folderPath, func(path string, info os.FileInfo, err error) error {
			select {
			case <-stream.Context().Done():
				log.Printf("Context cancelled during folder zipping for '%s': %v", folderPath, stream.Context().Err())
				return stream.Context().Err()
			default:
			}

			if err != nil {
				log.Printf("Error accessing path '%s' during walk: %v. Skipping.", path, err)
				if os.IsPermission(err) {
					return nil
				}
				return err
			}

			relPath, err := filepath.Rel(folderPath, path)
			if err != nil {
				log.Printf("Error getting relative path for '%s' (base '%s'): %v. Skipping.", path, folderPath, err)
				return nil
			}
			if relPath == "." {
				return nil
			}

			header, err := zip.FileInfoHeader(info)
			if err != nil {
				log.Printf("Error creating zip header for '%s': %v. Skipping.", path, err)
				return nil
			}
			header.Name = filepath.ToSlash(relPath)
			if info.IsDir() {
				header.Name += "/"
				header.Method = zip.Store
			} else {
				header.Method = zip.Deflate
			}

			entryWriter, err := zipWriter.CreateHeader(header)
			if err != nil {
				log.Printf("Error creating zip entry for '%s': %v.", path, err)
				return err
			}

			if !info.IsDir() {
				file, errOpen := os.Open(path)
				if errOpen != nil {
					log.Printf("Error opening file '%s' for zipping: %v. Skipping.", path, errOpen)
					if os.IsPermission(errOpen) {
						return nil
					}
					return errOpen
				}
				defer file.Close() // Close file after copying
				_, errCopy := io.Copy(entryWriter, file)
				if errCopy != nil {
					log.Printf("Error copying file content for '%s' to zip: %v.", path, errCopy)
					return errCopy
				}
			}
			return nil
		})

		if walkErr != nil {
			log.Printf("Directory walk or zipping failed for '%s': %v", folderPath, walkErr)
			if walkErr == context.Canceled || walkErr == stream.Context().Err() {
				log.Printf("Zipping cancelled for '%s' due to client disconnect.", folderPath)
			}
		} else {
			log.Printf("Finished zipping folder successfully: '%s'", folderPath)
		}
	}()

	buffer := make([]byte, 1024*64) // 64KB chunk size
	log.Printf("Starting stream of zip data for folder: '%s'", folderPath)

	for {
		if err := stream.Context().Err(); err != nil {
			log.Printf("Client cancelled download of zip for '%s': %v", folderPath, err)
			pipeReader.CloseWithError(err)
			return status.FromContextError(err).Err()
		}

		n, err := pipeReader.Read(buffer)
		if err != nil {
			if err == io.EOF {
				log.Printf("Finished streaming zip data for folder: '%s' (EOF from pipe).", folderPath)
				break
			}
			log.Printf("Error reading from zip pipe for '%s': %v", folderPath, err)
			return status.Errorf(codes.Internal, "Error reading zip data: %v", err)
		}

		chunkToSend := &pb.FileChunk{Content: buffer[:n]}

		if !firstChunkSent {
			// For zipped folders, the total size is not easily known beforehand without
			// zipping to a temp file first. We send TotalSize = 0, and the client
			// progress bar will remain indeterminate.
			chunkToSend.Metadata = &pb.FileChunkMetadata{
				TotalSize: 0, // Indicates unknown total size for client
			}
			log.Printf("Sending first chunk for zipped folder '%s' with metadata (TotalSize: 0 - indeterminate)", folderPath)
			firstChunkSent = true
		}

		sendErr := stream.Send(chunkToSend)
		if sendErr != nil {
			log.Printf("Error sending zip chunk for '%s': %v", folderPath, sendErr)
			pipeReader.CloseWithError(sendErr)
			if status.Code(sendErr) == codes.Canceled || status.Code(sendErr) == codes.Unavailable {
				return sendErr
			}
			return status.Errorf(codes.Internal, "Error sending zip chunk: %v", sendErr)
		}
	}
	log.Printf("Successfully streamed zip archive for folder: '%s'", folderPath)
	return nil
}

// getWindowsDrives lists available drive letters on Windows.
func getWindowsDrives() []*pb.FSNode {
	log.Println("Detecting Windows drives...")
	var drives []*pb.FSNode
	for _, driveLetter := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		path := string(driveLetter) + ":" + string(os.PathSeparator)
		fileInfo, err := os.Stat(path)
		if err == nil && fileInfo.IsDir() {
			log.Printf("Found accessible drive: %s", path)
			hasChildren := false
			entries, readErr := os.ReadDir(path)
			if readErr == nil {
				hasChildren = len(entries) > 0
			} else {
				log.Printf("Could not read drive %s to check for children: %v (assuming no accessible children)", path, readErr)
			}

			drives = append(drives, &pb.FSNode{
				Path:        path,
				Name:        path, // For drives, path and name are the same
				Type:        pb.FSNode_FOLDER,
				HasChildren: hasChildren,
				Size:        0,
			})
		}
	}
	if len(drives) == 0 {
		log.Println("No accessible Windows drives found.")
	}
	return drives
}

// getPosixRoot returns the root directory node for POSIX-like systems.
func getPosixRoot() []*pb.FSNode {
	log.Println("Returning POSIX root '/'")
	hasChildren := false
	entries, err := os.ReadDir("/")
	if err == nil {
		hasChildren = len(entries) > 0
	} else {
		log.Printf("Warning: Cannot read root directory '/' to check for children: %v", err)
	}
	return []*pb.FSNode{
		{
			Path:        "/",
			Name:        "/", // For root, path and name are the same
			Type:        pb.FSNode_FOLDER,
			HasChildren: hasChildren,
			Size:        0,
		},
	}
}

// listDirectoryContents reads and returns nodes for files and subdirectories.
func listDirectoryContents(dirPath string) ([]*pb.FSNode, error) {
	log.Printf("Listing contents of directory: %s", dirPath)
	var nodes []*pb.FSNode
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	log.Printf("Found %d entries in %s", len(entries), dirPath)
	for _, entry := range entries {
		nodePath := filepath.Join(dirPath, entry.Name())
		info, err := entry.Info()
		if err != nil {
			log.Printf("Could not get FileInfo for '%s': %v. Skipping.", nodePath, err)
			continue
		}

		node := &pb.FSNode{
			Path: nodePath,
			Name: entry.Name(), // Use the entry's name
			Size: info.Size(),
		}

		if entry.IsDir() {
			node.Type = pb.FSNode_FOLDER
			subEntries, subErr := os.ReadDir(node.Path)
			if subErr != nil {
				log.Printf("Error reading subdirectory '%s' for HasChildren check: %v. Assuming no accessible children.", node.Path, subErr)
				node.HasChildren = false
			} else {
				node.HasChildren = len(subEntries) > 0
			}
		} else {
			node.Type = pb.FSNode_FILE
			node.HasChildren = false
		}
		nodes = append(nodes, node)
	}
	log.Printf("Finished listing '%s'. Found %d valid nodes.", dirPath, len(nodes))
	return nodes, nil
}

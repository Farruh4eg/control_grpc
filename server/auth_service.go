package main

import (
	"context"
	"fmt"

	pb "control_grpc/gen/proto" // Assuming this path is correct
)

// Login handles user authentication.
func (s *server) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	username := req.GetUsername()
	password := req.GetPassword()
	fmt.Printf("Received login request for user: %s\n", username)

	if isValidUser(username, password) {
		return &pb.LoginResponse{Success: true}, nil
	}
	return &pb.LoginResponse{Success: false, ErrorMessage: "Invalid credentials"}, nil
}

// isValidUser checks if the provided username and password are valid.
// Replace this with your actual authentication logic (e.g., database lookup).
func isValidUser(username, password string) bool {
	// Placeholder validation logic
	return username == "test" && password == "password"
}

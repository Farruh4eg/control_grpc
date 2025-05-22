package main

import (
	"context"
	"fmt"

	pb "control_grpc/gen/proto"
)

func (s *server) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	username := req.GetUsername()
	password := req.GetPassword()
	fmt.Printf("Received login request for user: %s\n", username)

	if isValidUser(username, password) {
		return &pb.LoginResponse{Success: true}, nil
	}
	return &pb.LoginResponse{Success: false, ErrorMessage: "Invalid credentials"}, nil
}

func isValidUser(username, password string) bool {

	return username == "test" && password == "password"
}

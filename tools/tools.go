//go:build tools
// +build tools

package tools

import (
	_ "github.com/nathan77886/go-tools/cmd/protoc-gen-gamerpc-registry"
	_ "google.golang.org/grpc/cmd/protoc-gen-go-grpc"
	_ "google.golang.org/protobuf/cmd/protoc-gen-go"
)

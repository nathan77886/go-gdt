module github.com/nathonNot/go-gdt

go 1.24

tool (
	github.com/nathan77886/go-tools/cmd/protoc-gen-gamerpc-registry
	google.golang.org/grpc/cmd/protoc-gen-go-grpc
	google.golang.org/protobuf/cmd/protoc-gen-go
)

require github.com/sirupsen/logrus v1.9.3

require golang.org/x/sys v0.0.0-20220715151400-c0bba94af5f8 // indirect

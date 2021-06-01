GOBIN_TOOL	= $(shell which gobin || echo $(GOBIN)/gobin)

.PHONY: all
all: protogen

.PHONY: protogen
protogen: faultinjector.pb.go faultinjector_grpc.pb.go

faultinjector.pb.go faultinjector_grpc.pb.go: faultinjector.proto Makefile
	protoc --plugin=protoc-gen-titusgo=$(shell $(GOBIN_TOOL) -p github.com/golang/protobuf/protoc-gen-go@v1.4.2) \
		--titusgo_out=plugins=grpc:. \
		faultinjector.proto
#	protoc \
#		--plugin=protoc-gen-go=$(shell $(GOBIN_TOOL) -p google.golang.org/protobuf/cmd/protoc-gen-go@v1.26.0) \
#		#--plugin=protoc-gen-go-grpc=$(shell $(GOBIN_TOOL) -p github.com/grpc/grpc-go/cmd/protoc-gen-go-grpc@1.4.2) \
#		--go_out=. --go_opt=paths=source_relative \
#     #  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
#        --go_out=plugins=grpc:.  \
#        faultinjector.proto


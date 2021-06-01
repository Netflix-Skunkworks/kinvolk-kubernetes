package service

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"

	"google.golang.org/grpc/grpclog"
)

const (
	FaultInjectorListenerEnv = "FAULT_INJECTOR_LISTENER"
)

func init() {
	addr := os.Getenv(FaultInjectorListenerEnv)
	if addr == "" {
		return
	}
	go runListener(addr, defaultFaultInjectionServer)
}
func isDefaultEnabled() bool {
	return os.Getenv(FaultInjectorListenerEnv) != ""
}

func runListener(addr string, faultinjectionserver *grpc.Server) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// We backoff here, because there's some ugliness if
	var listener net.Listener
	for i := 0; i < 5; i++ {
		var err error
		listener, err = net.Listen("tcp", addr)
		if err == nil {
			listen(ctx, listener, faultinjectionserver)
			return
		}
		time.Sleep(1 * time.Second)
	}
	grpclog.Warning("Failed to complete listening")
}

func listen(ctx context.Context, listener net.Listener, faultinjectionserver *grpc.Server) {
	err := faultinjectionserver.Serve(listener)
	if err != nil && !errors.Is(err, context.Canceled) {
		grpclog.Error("Fault injection server could not listen: %s", err.Error())
	}
}

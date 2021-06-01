package service

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	faultinjectorpb "github.com/Netflix-skunkworks/grpc_fault/faultinjector"
	"github.com/antonmedv/expr"
	"github.com/antonmedv/expr/vm"
	proto "github.com/golang/protobuf/proto" // nolint: staticcheck
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	defaultFaultInjectionServer = grpc.NewServer()
	defaultInterceptor          = NewInterceptor(context.TODO(), defaultFaultInjectionServer)
)

type FaultInjectorInterceptor interface {
	UnaryClientInterceptor(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error
	// Only the method is passed to the fault injection inspection
	StreamClientInterceptor(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error)
	RegisterService(service grpc.ServiceDesc)
}

// GenerateInterceptor can be used to register an interceptor for a given grpc service. If a service is not registered
// the interceptor methods will error out
func RegisterInterceptor(service grpc.ServiceDesc) {
	if !isDefaultEnabled() {
		return
	}
	defaultInterceptor.RegisterService(service)
}

func UnaryClientInterceptor(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	return defaultInterceptor.UnaryClientInterceptor(ctx, method, req, reply, cc, invoker, opts...)
}

func StreamClientInterceptor(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return defaultInterceptor.StreamClientInterceptor(ctx, desc, cc, method, streamer, opts...)
}

// NewInterceptor can be used to generate a single instance of the interceptor
func NewInterceptor(ctx context.Context, server *grpc.Server) FaultInjectorInterceptor {
	fii := &faultInjectorInterceptor{}
	fis := &faultInjectorServer{
		fii: fii,
	}

	faultinjectorpb.RegisterFaultInjectorServer(server, fis)

	return fii
}

type faultInjectorInterceptor struct {
	// Map of services -> *serviceFaultInjectorInterceptor
	services sync.Map
}

func (f *faultInjectorInterceptor) RegisterService(service grpc.ServiceDesc) {
	sfii := &serviceFaultInjectorInterceptor{
		service: service,
	}
	for idx := range service.Methods {
		method := service.Methods[idx]
		sfii.methods.Store(method.MethodName, &methodFaultInjectorInterceptor{})
	}
	f.services.LoadOrStore(service.ServiceName, sfii)
}

func (f *faultInjectorInterceptor) UnaryClientInterceptor(ctx context.Context, methodAndService string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	// Can either be:
	// * /helloworld.Greeter/SayHello
	// Or:
	// * helloworld.Greeter/SayHello
	splitMethod := strings.Split(strings.TrimLeft(methodAndService, "/"), "/")
	serviceName := splitMethod[0]
	methodName := splitMethod[1]
	msg := rr{}
	if val, ok := req.(proto.Message); ok {
		msg.req = val
	}

	var messageChan chan rr
	val, ok := f.services.Load(serviceName)
	if ok {
		sfii := val.(*serviceFaultInjectorInterceptor)
		val, ok = sfii.methods.Load(methodName)
		if ok {
			mfii := val.(*methodFaultInjectorInterceptor)
			mfii.lock.RLock()
			program := mfii.program
			messageChan = mfii.messageChan
			mfii.lock.RUnlock()
			if program != nil {
				val, err := vm.Run(program, makeEnv(req))
				if err != nil {
					log.Ctx(ctx).WithLevel(zerolog.WarnLevel).Err(err).Msg("Could not run program")
				} else if val != nil {
					err, ok := val.(error)
					if !ok {
						log.Ctx(ctx).WithLevel(zerolog.WarnLevel).Str("type", fmt.Sprintf("%T", err)).Msg("Received unexpected type from program")
					}
					if status.Code(err) != codes.OK {
						msg.err = err
						select {
						case messageChan <- msg:
						default:
						}
						return err
					}
				}
			}
		}
	}
	err := invoker(ctx, methodAndService, req, reply, cc, opts...)
	msg.err = err
	if val, ok := reply.(proto.Message); ok {
		msg.resp = val
	}
	select {
	case messageChan <- msg:
	default:
	}

	return err
}

func (f *faultInjectorInterceptor) StreamClientInterceptor(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	log.Ctx(ctx).WithLevel(zerolog.InfoLevel).Str("method", method).Msg("Invoked stream client interceptor")
	return streamer(ctx, desc, cc, method, opts...)
}

type serviceFaultInjectorInterceptor struct {
	service grpc.ServiceDesc
	// Map of method name -> methodFaultInjectorInterceptor
	methods sync.Map
}

type methodFaultInjectorInterceptor struct {
	lock        sync.RWMutex
	expression  string
	program     *vm.Program
	messageChan chan rr
}

type faultInjectorServer struct {
	fii *faultInjectorInterceptor
}

func (f *faultInjectorServer) EnumerateServices(ctx context.Context, request *faultinjectorpb.EnumerateServicesRequest) (*faultinjectorpb.EnumerateServicesResponse, error) {
	resp := faultinjectorpb.EnumerateServicesResponse{}
	f.fii.services.Range(func(key, value interface{}) bool {
		servicename := key.(string)
		methods := []*faultinjectorpb.Method{}

		sfii := value.(*serviceFaultInjectorInterceptor)
		sfii.methods.Range(func(key, value interface{}) bool {
			methodName := key.(string)
			mfii := value.(*methodFaultInjectorInterceptor)
			mfii.lock.RLock()
			methods = append(methods, &faultinjectorpb.Method{
				Name:       methodName,
				Expression: mfii.expression,
			})
			mfii.lock.RUnlock()

			return true
		})
		resp.Services = append(resp.Services, &faultinjectorpb.Service{
			Name:    servicename,
			Methods: methods,
		})

		return true
	})

	return &resp, nil
}
func (f *faultInjectorServer) RegisterFault(ctx context.Context, req *faultinjectorpb.RegisterFaultRequest) (*faultinjectorpb.RegisterFaultResponse, error) {
	val, ok := f.fii.services.Load(req.Service)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "service %s not found", req.Service)
	}

	service := val.(*serviceFaultInjectorInterceptor)
	val, ok = service.methods.Load(req.Method)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "method %s not found", req.Method)
	}
	mfii := val.(*methodFaultInjectorInterceptor)

	// ht is the interface of the service.
	ht := reflect.TypeOf(service.service.HandlerType).Elem()
	reflectedMethod, ok := ht.MethodByName(req.Method)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "method %s not found on reflection", req.Method)
	}

	fakeReq := reflect.New(reflectedMethod.Type.In(1)).Interface()
	env := makeEnv(fakeReq)
	pgrm, err := expr.Compile(req.Expression, expr.Env(env), expr.Optimize(true))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Could not compile the program: %s", err.Error())
	}

	mfii.lock.Lock()
	mfii.program = pgrm
	mfii.expression = req.Expression
	mfii.lock.Unlock()

	return &faultinjectorpb.RegisterFaultResponse{}, nil
}

type rr struct {
	req  proto.Message
	resp proto.Message
	err  error
}

func (f *faultInjectorServer) Listen(req *faultinjectorpb.ListenRequest, resp faultinjectorpb.FaultInjector_ListenServer) error {
	listenerChannel := make(chan rr, 100)

	val, ok := f.fii.services.Load(req.Service)
	if !ok {
		return status.Errorf(codes.NotFound, "Service %s not found", req.Service)
	}
	sfii := val.(*serviceFaultInjectorInterceptor)

	val, ok = sfii.methods.Load(req.Method)
	if !ok {
		return status.Errorf(codes.NotFound, "Method %s not found", req.Method)
	}

	mfii := val.(*methodFaultInjectorInterceptor)
	mfii.lock.Lock()
	if mfii.messageChan != nil {
		mfii.lock.Unlock()
		return status.Errorf(codes.AlreadyExists, "Listener already exists for %s / %s", req.Service, req.Method)
	}
	mfii.messageChan = listenerChannel
	mfii.lock.Unlock()

	defer func() {
		mfii.lock.Lock()
		defer mfii.lock.Unlock()
		mfii.messageChan = nil
	}()

	for {
		select {
		case <-resp.Context().Done():
			return nil
		case rr := <-listenerChannel:
			val := &faultinjectorpb.ListenResponse{
				Request: rr.req.String(),
			}
			if rr.resp != nil {
				val.Reply = rr.resp.String()
			}
			if rr.err != nil {
				val.Error = rr.err.Error()
			}

			err := resp.Send(val)
			if err != nil {
				return err
			}
		}
	}
}

func makeCodeFunction(c codes.Code) func(msg string) error {
	return func(msg string) error {
		return status.Error(c, msg)
	}
}

func makeEnv(req interface{}) map[string]interface{} {
	env := map[string]interface{}{
		"req": req,
		"OK": func() error {
			return nil
		},
		"Canceled":           makeCodeFunction(codes.Canceled),
		"Unknown":            makeCodeFunction(codes.Unknown),
		"InvalidArgument":    makeCodeFunction(codes.InvalidArgument),
		"DeadlineExceeded":   makeCodeFunction(codes.DeadlineExceeded),
		"NotFound":           makeCodeFunction(codes.NotFound),
		"AlreadyExists":      makeCodeFunction(codes.AlreadyExists),
		"PermissionDenied":   makeCodeFunction(codes.PermissionDenied),
		"ResourceExhausted":  makeCodeFunction(codes.ResourceExhausted),
		"FailedPrecondition": makeCodeFunction(codes.FailedPrecondition),
		"Aborted":            makeCodeFunction(codes.Aborted),
		"OutOfRange":         makeCodeFunction(codes.OutOfRange),
		"Unimplemented":      makeCodeFunction(codes.Unimplemented),
		"Internal":           makeCodeFunction(codes.Internal),
		"Unavailable":        makeCodeFunction(codes.Unavailable),
		"DataLoss":           makeCodeFunction(codes.DataLoss),
		"Unauthenticated":    makeCodeFunction(codes.Unauthenticated),
	}

	return env
}

func (f *faultInjectorServer) RemoveFault(ctx context.Context, req *faultinjectorpb.RemoveFaultRequest) (*faultinjectorpb.RemoveFaultResponse, error) {
	val, ok := f.fii.services.Load(req.Service)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "service %s not found", req.Service)
	}
	service := val.(*serviceFaultInjectorInterceptor)

	val, ok = service.methods.Load(req.Method)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "method %s not found", req.Service)
	}
	method := val.(*methodFaultInjectorInterceptor)
	method.lock.Lock()
	defer method.lock.Unlock()
	method.expression = ""
	method.program = nil

	return &faultinjectorpb.RemoveFaultResponse{}, nil
}

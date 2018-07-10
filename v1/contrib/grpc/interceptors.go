package grpc

import (
	"io"
	"strings"
	"sync"
	"time"

	"github.com/appoptics/appoptics-apm-go/v1/ao"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func actionFromMethod(method string) string {
	mParts := strings.Split(method, "/")

	return mParts[len(mParts)-1]
}

func tracingContext(ctx context.Context, serverName string, methodName string, statusCode *int) context.Context {

	action := actionFromMethod(methodName)

	xtID := ""
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		xt, ok := md[ao.HTTPHeaderName]
		if ok {
			xtID = xt[0]
		}
	}

	t := ao.NewTraceFromID(serverName, xtID, func() ao.KVMap {
		return ao.KVMap{
			"Method":     "POST",
			"Controller": serverName,
			"Action":     action,
			"URL":        methodName,
			"Status":     statusCode,
		}
	})
	t.SetMethod("POST")
	t.SetTransactionName(serverName + "." + action)
	t.SetStartTime(time.Now())

	return ao.NewContext(ctx, t)
}

func UnaryServerInterceptor(serverName string) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		var err error
		var resp interface{}
		var statusCode = 200
		ctx = tracingContext(ctx, serverName, info.FullMethod, &statusCode)
		defer ao.EndTrace(ctx)
		resp, err = handler(ctx, req)
		if err != nil {
			statusCode = 500
			ao.Err(ctx, err)
		}
		return resp, err
	}
}

// WrappedServerStream from the grpc_middleware project
// because it seemed too small a swatch to bring in a dependency
type WrappedServerStream struct {
	grpc.ServerStream
	WrappedContext context.Context
}

func (w *WrappedServerStream) Context() context.Context {
	return w.WrappedContext
}

func WrapServerStream(stream grpc.ServerStream) *WrappedServerStream {
	if existing, ok := stream.(*WrappedServerStream); ok {
		return existing
	}
	return &WrappedServerStream{ServerStream: stream, WrappedContext: stream.Context()}
}

func StreamServerInterceptor(serverName string) grpc.StreamServerInterceptor {
	return func(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		var err error
		var statusCode = 200
		newCtx := tracingContext(stream.Context(), serverName, info.FullMethod, &statusCode)
		defer ao.EndTrace(newCtx)
		// if lg.IsDebug() {
		//	sp := ao.FromContext(newCtx)
		//	lg.Debug("server stream starting", "xtrace", sp.MetadataString())
		// }
		wrappedStream := WrapServerStream(stream)
		wrappedStream.WrappedContext = newCtx
		err = handler(srv, wrappedStream)
		if err == io.EOF {
			return nil
		} else if err != nil {
			statusCode = 500
			ao.Err(newCtx, err)
		}
		return err
	}
}

func UnaryClientInterceptor(target string, serviceName string) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, resp interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		action := actionFromMethod(method)
		span := ao.BeginRPCSpan(ctx, action, "grpc", serviceName, target)
		defer span.End()
		xtID := span.MetadataString()
		if len(xtID) > 0 {
			ctx = metadata.AppendToOutgoingContext(ctx, ao.HTTPHeaderName, xtID)
		}
		err := invoker(ctx, method, req, resp, cc, opts...)
		if err != nil {
			span.Err(err)
			return err
		}
		return nil
	}
}

func StreamClientInterceptor(target string, serviceName string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		action := actionFromMethod(method)
		span := ao.BeginRPCSpan(ctx, action, "grpc", serviceName, target)
		xtID := span.MetadataString()
		// lg.Debug("stream client interceptor", "x-trace", xtID)
		if len(xtID) > 0 {
			ctx = metadata.AppendToOutgoingContext(ctx, ao.HTTPHeaderName, xtID)
		}
		clientStream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			closeSpan(span, err)
			return nil, err
		}
		return &tracedClientStream{ClientStream: clientStream, span: span}, nil
	}
}

type tracedClientStream struct {
	grpc.ClientStream
	mu     sync.Mutex
	closed bool
	span   ao.Span
}

func (s *tracedClientStream) Header() (metadata.MD, error) {
	h, err := s.ClientStream.Header()
	if err != nil {
		s.closeSpan(err)
	}
	return h, err
}

func (s *tracedClientStream) SendMsg(m interface{}) error {
	err := s.ClientStream.SendMsg(m)
	if err != nil {
		s.closeSpan(err)
	}
	return err
}

func (s *tracedClientStream) CloseSend() error {
	err := s.ClientStream.CloseSend()
	if err != nil {
		s.closeSpan(err)
	}
	return err
}

func (s *tracedClientStream) RecvMsg(m interface{}) error {
	err := s.ClientStream.RecvMsg(m)
	if err != nil {
		s.closeSpan(err)
	}
	return err
}

func (s *tracedClientStream) closeSpan(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		closeSpan(s.span, err)
		s.closed = true
	}
}

func closeSpan(span ao.Span, err error) {
	// lg.Debug("closing span", "err", err.Error())
	if err != nil && err != io.EOF {
		span.Err(err)
	}
	span.End()
}

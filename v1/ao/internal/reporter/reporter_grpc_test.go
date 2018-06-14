// Copyright (C) 2017 Librato, Inc. All rights reserved.

package reporter

import (
	"errors"
	"net"
	"os"
	"path"
	"testing"

	pb "github.com/appoptics/appoptics-apm-go/v1/ao/internal/reporter/collector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var (
	testKeyPath  = path.Join(os.Getenv("GOPATH"), "src/github.com/appoptics/appoptics-apm-go/v1/ao/internal/reporter")
	testKeyFile  = path.Join(testKeyPath, "localhost.key")
	testCertFile = path.Join(testKeyPath, "localhost.crt")
)

type TestGRPCServer struct {
	t          *testing.T
	grpcServer *grpc.Server
	addr       string
	events     []*pb.MessageRequest
	metrics    []*pb.MessageRequest
	status     []*pb.MessageRequest
	pings      int
}

func StartTestGRPCServer(t *testing.T, addr string) *TestGRPCServer {
	lis, err := net.Listen("tcp", addr)
	require.NoError(t, err)

	// Create the TLS credentials
	creds, err := credentials.NewServerTLSFromFile(testCertFile, testKeyFile)
	require.NoError(t, err, "could not load TLS keys")
	assert.NotNil(t, creds)

	// Create the gRPC server with the credentials
	grpcServer := grpc.NewServer(grpc.Creds(creds))
	assert.NotNil(t, grpcServer)
	testServer := &TestGRPCServer{t: t, grpcServer: grpcServer, addr: addr}
	pb.RegisterTraceCollectorServer(grpcServer, testServer)
	require.NoError(t, err)

	go grpcServer.Serve(lis)
	return testServer
}

func (s *TestGRPCServer) Stop() { s.grpcServer.Stop() }

func (s *TestGRPCServer) PostEvents(ctx context.Context, req *pb.MessageRequest) (*pb.MessageResult, error) {
	s.t.Logf("TestGRPCServer.PostEvents req %+v", req)
	s.events = append(s.events, req)
	return &pb.MessageResult{Result: pb.ResultCode_OK}, nil
}

func (s *TestGRPCServer) PostMetrics(ctx context.Context, req *pb.MessageRequest) (*pb.MessageResult, error) {
	s.t.Logf("TestGRPCServer.PostMetrics req %+v", req)
	s.metrics = append(s.metrics, req)
	return &pb.MessageResult{Result: pb.ResultCode_OK}, nil
}

func (s *TestGRPCServer) PostStatus(ctx context.Context, req *pb.MessageRequest) (*pb.MessageResult, error) {
	s.t.Logf("TestGRPCServer.PostStatus req %+v", req)
	s.status = append(s.status, req)
	return &pb.MessageResult{Result: pb.ResultCode_OK}, nil
}

func (s *TestGRPCServer) GetSettings(ctx context.Context, req *pb.SettingsRequest) (*pb.SettingsResult, error) {
	s.t.Logf("TestGRPCServer.GetSettings req %+v", req)
	return &pb.SettingsResult{
		Result: pb.ResultCode_OK,
		Settings: []*pb.OboeSetting{{
			Type: pb.OboeSettingType_DEFAULT_SAMPLE_RATE,
			// Flags:     XXX,
			// Layer:     "", // default, specifically not setting layer/service
			// Timestamp: XXX,
			Value:     1000000,
			Arguments: map[string][]byte{
				//   "BucketCapacity": XXX,
				//   "BucketRate":     XXX,
			},
			Ttl: 120,
		}},
	}, nil
}
func (s *TestGRPCServer) Ping(context.Context, *pb.PingRequest) (*pb.MessageResult, error) {
	s.t.Logf("TestGRPCServer.Ping")
	s.pings++
	return &pb.MessageResult{Result: pb.ResultCode_OK}, nil
}

func TestGrpcNewReporter(t *testing.T) {
}

func TestVerifyServiceKey(t *testing.T) {

	keyPairs := map[string]error{
		"1234567890abcdef:Go": nil,
		"":                  errors.New("invalid service key: empty string"),
		"abc:Go":            nil,
		"abcd1234:Go":       nil,
		"1234567890abcdef":  errors.New("invalid service key: no colon found or tk/service name is missing"),
		"1234567890abcdef:": errors.New("invalid service key: no colon found or tk/service name is missing"),
		":Go":               errors.New("invalid service key: no colon found or tk/service name is missing"),
		"abc:123:Go":        errors.New("invalid service key: no colon found or tk/service name is missing"),
	}

	for key, err := range keyPairs {
		assert.Equal(t, err, verifyServiceKey(key))
	}
}

func TestMaskServiceKey(t *testing.T) {
	keyPairs := map[string]string{
		"1234567890abcdef:Go": "1234********cdef:Go",
		"abc:Go":              "abc:Go",
		"abcd1234:Go":         "abcd1234:Go",
	}

	for key, masked := range keyPairs {
		assert.Equal(t, masked, maskServiceKey(key))
	}
}

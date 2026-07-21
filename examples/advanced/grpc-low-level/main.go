package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/camunda/orchestration-cluster-api-go/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type bearerToken struct {
	token string
}

func (b bearerToken) GetRequestMetadata(
	context.Context,
	...string,
) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + b.token}, nil
}

func (bearerToken) RequireTransportSecurity() bool {
	return true
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "low-level gRPC example:", err)
		os.Exit(1)
	}
}

func run() error {
	address := envOr("CAMUNDA_GRPC_ADDRESS", "localhost:26500")
	plaintext := strings.EqualFold(os.Getenv("CAMUNDA_GRPC_INSECURE"), "true")

	var transportCredentials credentials.TransportCredentials
	if plaintext {
		transportCredentials = insecure.NewCredentials()
	} else {
		transportCredentials = credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
		})
	}

	dialOptions := []grpc.DialOption{
		grpc.WithTransportCredentials(transportCredentials),
	}
	if token := os.Getenv("CAMUNDA_ACCESS_TOKEN"); token != "" {
		if plaintext {
			return errors.New("refusing to send CAMUNDA_ACCESS_TOKEN over plaintext gRPC")
		}
		dialOptions = append(dialOptions,
			grpc.WithPerRPCCredentials(bearerToken{token: token}))
	}

	conn, err := grpc.NewClient(address, dialOptions...)
	if err != nil {
		return fmt.Errorf("configure gRPC client for %s: %w", address, err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "close gRPC connection:", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	gateway := pb.NewGatewayClient(conn)
	topology, err := gateway.Topology(ctx, &pb.TopologyRequest{})
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated:
			return fmt.Errorf("topology authentication failed: obtain a fresh access token: %w", err)
		case codes.PermissionDenied:
			return fmt.Errorf("token is not authorized to read topology: %w", err)
		case codes.DeadlineExceeded:
			return fmt.Errorf("topology request to %s timed out: %w", address, err)
		default:
			return fmt.Errorf("get topology from %s: %w", address, err)
		}
	}

	fmt.Printf(
		"cluster %q: gateway %s, %d broker(s), %d partition(s), replication factor %d\n",
		topology.GetClusterId(),
		topology.GetGatewayVersion(),
		topology.GetClusterSize(),
		topology.GetPartitionsCount(),
		topology.GetReplicationFactor(),
	)
	for _, broker := range topology.GetBrokers() {
		fmt.Printf("broker %d at %s:%d owns %d partition(s)\n",
			broker.GetNodeId(), broker.GetHost(), broker.GetPort(), len(broker.GetPartitions()))
	}
	return nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	internal "github.com/lautaroblasco23/imagestore/internal"
	pb "github.com/lautaroblasco23/imagestore/proto/imagestore/v1"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const (
	dbPath    = "./images/imagestore.db"
	imagesDir = "./images"
)

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	baseURL := getEnv("BASE_URL", "http://localhost:8087")

	bindAddr := getEnv("BIND_ADDR", "127.0.0.1")
	grpcAddr := bindAddr + ":50051"
	httpAddr := bindAddr + ":8087"

	// Always create the images directory: needed for the SQLite database file.
	if err := os.MkdirAll(imagesDir, 0o750); err != nil {
		log.Fatalf("failed to create images directory: %v", err)
	}

	db, err := internal.NewDB(dbPath)
	if err != nil {
		log.Fatalf("failed to create database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("error closing database: %v", err)
		}
	}()

	storage := initStorage()

	handler := internal.NewImageHandler(db, storage, baseURL)

	grpcServer := grpc.NewServer()
	pb.RegisterImageServiceServer(grpcServer, handler)
	reflection.Register(grpcServer)

	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/images/", handler.ServeHTTP)
	httpMux.HandleFunc("/health", handler.HealthCheck)

	go func() {
		listener, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			log.Fatalf("failed to listen on %s: %v", grpcAddr, err)
		}
		log.Printf("gRPC server listening on %s", grpcAddr)
		if err := grpcServer.Serve(listener); err != nil {
			log.Fatalf("failed to serve gRPC: %v", err)
		}
	}()

	go func() {
		httpServer := &http.Server{
			Addr:         httpAddr,
			Handler:      httpMux,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
		log.Printf("HTTP server listening on %s", httpAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("failed to serve HTTP: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down servers...")
	grpcServer.GracefulStop()
	log.Println("servers stopped")
}

// initStorage selects the storage backend based on the STORAGE_BACKEND env var.
// Supported values: "local" (default), "s3".
func initStorage() internal.StorageBackend {
	switch getEnv("STORAGE_BACKEND", "local") {
	case "s3":
		return initS3Storage()
	default:
		log.Printf("using local file storage backend")
		return internal.NewStorage(imagesDir)
	}
}

func initS3Storage() internal.StorageBackend {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(getEnv("S3_REGION", "us-east-1")),
	)
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	opts := []func(*awss3.Options){}
	if endpoint := getEnv("S3_ENDPOINT", ""); endpoint != "" {
		opts = append(opts, func(o *awss3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true // required for LocalStack and path-style S3 endpoints
		})
	}

	client := awss3.NewFromConfig(cfg, opts...)
	bucket := getEnv("S3_BUCKET", "imagestore-bucket")

	s3Storage, err := internal.NewS3Storage(client, bucket)
	if err != nil {
		log.Fatalf("failed to initialize S3 storage: %v", err)
	}

	log.Printf("using S3 storage backend (bucket: %s)", bucket)
	return s3Storage
}

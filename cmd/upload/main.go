package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	pb "github.com/lautaroblasco23/imagestore/proto/imagestore/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: upload <image-path>")
	}
	filePath := os.Args[1]

	f, err := os.Open(filePath)
	if err != nil {
		log.Fatalf("open file: %v", err)
	}
	defer f.Close()

	conn, err := grpc.NewClient("localhost:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	client := pb.NewImageServiceClient(conn)
	stream, err := client.UploadImage(context.Background())
	if err != nil {
		log.Fatalf("stream: %v", err)
	}

	// Send metadata first
	if err := stream.Send(&pb.UploadImageRequest{
		Data: &pb.UploadImageRequest_Metadata{
			Metadata: &pb.ImageMetadataInput{
				UserId:      "test-user",
				Filename:    filePath,
				ContentType: "image/png",
			},
		},
	}); err != nil {
		log.Fatalf("send metadata: %v", err)
	}

	// Stream file in 64 KB chunks
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if sendErr := stream.Send(&pb.UploadImageRequest{
				Data: &pb.UploadImageRequest_Chunk{Chunk: buf[:n]},
			}); sendErr != nil {
				log.Fatalf("send chunk: %v", sendErr)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("read file: %v", err)
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		log.Fatalf("close: %v", err)
	}

	fmt.Printf("image_id:      %s\n", resp.ImageId)
	fmt.Printf("url:           %s\n", resp.Url)
	fmt.Printf("thumbnail_url: %s\n", resp.ThumbnailUrl)
	fmt.Printf("size_bytes:    %d\n", resp.SizeBytes)
}

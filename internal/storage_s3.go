package internal

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"log"
	"math"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

type S3Storage struct {
	client        *s3.Client
	bucket        string
	thumbnailSize uint
}

// NewS3Storage creates an S3Storage and ensures the bucket exists.
func NewS3Storage(client *s3.Client, bucket string) (*S3Storage, error) {
	ctx := context.Background()

	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		// Bucket not found or not accessible — attempt to create it.
		if _, createErr := client.CreateBucket(ctx, &s3.CreateBucketInput{
			Bucket: aws.String(bucket),
		}); createErr != nil {
			return nil, fmt.Errorf("failed to create S3 bucket %q: %w", bucket, createErr)
		}
		log.Printf("created S3 bucket %q", bucket)
	}

	return &S3Storage{
		client:        client,
		bucket:        bucket,
		thumbnailSize: 200,
	}, nil
}

func (s *S3Storage) SaveImage(userID, filename string, reader io.Reader) (imageID, originalPath, thumbnailPath string, width, height int32, size int64, err error) {
	imageID = uuid.New().String()

	data, err := io.ReadAll(reader)
	if err != nil {
		return "", "", "", 0, 0, 0, fmt.Errorf("failed to read image: %w", err)
	}
	size = int64(len(data))

	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", "", "", 0, 0, 0, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := img.Bounds()
	dx, dy := bounds.Dx(), bounds.Dy()
	if dx < 0 || dy < 0 || dx > math.MaxInt32 || dy > math.MaxInt32 {
		return "", "", "", 0, 0, 0, fmt.Errorf("invalid image dimensions")
	}
	width = int32(dx)
	height = int32(dy)

	ext := getSafeExtension(filename, format)
	// Use path.Join (not filepath.Join) to guarantee forward-slash S3 keys.
	originalKey := path.Join("originals", userID, imageID+ext)
	thumbnailKey := path.Join("thumbnails", userID, imageID+"_thumb.webp")

	ctx := context.Background()

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(originalKey),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/octet-stream"),
	})
	if err != nil {
		return "", "", "", 0, 0, 0, fmt.Errorf("failed to upload original to S3: %w", err)
	}

	thumbData, err := generateThumbnail(img, s.thumbnailSize)
	if err != nil {
		if delErr := s.deleteKey(ctx, originalKey); delErr != nil {
			log.Printf("failed to delete S3 key %q on rollback: %v", originalKey, delErr)
		}
		return "", "", "", 0, 0, 0, fmt.Errorf("failed to generate thumbnail: %w", err)
	}

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(thumbnailKey),
		Body:        bytes.NewReader(thumbData),
		ContentType: aws.String("image/webp"),
	})
	if err != nil {
		if delErr := s.deleteKey(ctx, originalKey); delErr != nil {
			log.Printf("failed to delete S3 key %q on rollback: %v", originalKey, delErr)
		}
		return "", "", "", 0, 0, 0, fmt.Errorf("failed to upload thumbnail to S3: %w", err)
	}

	return imageID, originalKey, thumbnailKey, width, height, size, nil
}

func (s *S3Storage) ReadImage(imagePath string) ([]byte, error) {
	ctx := context.Background()
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(imagePath),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get object from S3: %w", err)
	}
	defer result.Body.Close()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read S3 object body: %w", err)
	}
	return data, nil
}

func (s *S3Storage) DeleteImage(originalPath, thumbnailPath string) error {
	ctx := context.Background()
	if err := s.deleteKey(ctx, originalPath); err != nil {
		return fmt.Errorf("failed to delete original from S3: %w", err)
	}
	if err := s.deleteKey(ctx, thumbnailPath); err != nil {
		return fmt.Errorf("failed to delete thumbnail from S3: %w", err)
	}
	return nil
}

func (s *S3Storage) deleteKey(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

package internal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	pb "github.com/lautaroblasco23/imagestore/proto/imagestore/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ImageHandler struct {
	pb.UnimplementedImageServiceServer
	db      *DB
	storage StorageBackend
	baseURL string
}

func NewImageHandler(db *DB, storage StorageBackend, baseURL string) *ImageHandler {
	return &ImageHandler{
		db:      db,
		storage: storage,
		baseURL: baseURL,
	}
}

func (h *ImageHandler) UploadImage(stream pb.ImageService_UploadImageServer) error {
	var metadata *pb.ImageMetadataInput
	var buffer bytes.Buffer

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		switch data := req.Data.(type) {
		case *pb.UploadImageRequest_Metadata:
			metadata = data.Metadata
		case *pb.UploadImageRequest_Chunk:
			buffer.Write(data.Chunk)
		}
	}

	if metadata == nil {
		return fmt.Errorf("metadata not provided")
	}

	imageID, originalPath, thumbnailPath, width, height, size, err := h.storage.SaveImage(
		metadata.UserId,
		metadata.Filename,
		&buffer,
	)
	if err != nil {
		return err
	}

	img := &Image{
		ID:            imageID,
		UserID:        metadata.UserId,
		Filename:      metadata.Filename,
		ContentType:   metadata.ContentType,
		SizeBytes:     size,
		Width:         width,
		Height:        height,
		UploadedAt:    time.Now(),
		OriginalPath:  originalPath,
		ThumbnailPath: thumbnailPath,
	}

	if err := h.db.SaveImage(stream.Context(), img); err != nil {
		if err := h.storage.DeleteImage(originalPath, thumbnailPath); err != nil {
			log.Printf("failed to delete image on rollback: %v", err)
		}
		return err
	}

	return stream.SendAndClose(&pb.UploadImageResponse{
		ImageId:      imageID,
		Url:          fmt.Sprintf("%s/images/%s", h.baseURL, imageID),
		ThumbnailUrl: fmt.Sprintf("%s/images/%s?thumbnail=true", h.baseURL, imageID),
		SizeBytes:    size,
	})
}

func (h *ImageHandler) GetImageMetadata(ctx context.Context, req *pb.GetImageMetadataRequest) (*pb.ImageMetadata, error) {
	img, err := h.db.GetImage(ctx, req.ImageId)
	if err != nil {
		return nil, err
	}

	return &pb.ImageMetadata{
		ImageId:      img.ID,
		UserId:       img.UserID,
		Filename:     img.Filename,
		ContentType:  img.ContentType,
		SizeBytes:    img.SizeBytes,
		Width:        img.Width,
		Height:       img.Height,
		UploadedAt:   timestamppb.New(img.UploadedAt),
		Url:          fmt.Sprintf("%s/images/%s", h.baseURL, img.ID),
		ThumbnailUrl: fmt.Sprintf("%s/images/%s?thumbnail=true", h.baseURL, img.ID),
	}, nil
}

func (h *ImageHandler) ListImages(ctx context.Context, req *pb.ListImagesRequest) (*pb.ListImagesResponse, error) {
	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = 10
	}
	if pageSize > 100 {
		pageSize = 100
	}

	offset := 0
	if req.PageToken != "" {
		offset, _ = strconv.Atoi(req.PageToken)
	}

	images, err := h.db.ListImages(ctx, req.UserId, pageSize, offset)
	if err != nil {
		return nil, err
	}

	totalCount, err := h.db.CountImages(ctx, req.UserId)
	if err != nil {
		return nil, err
	}

	pbImages := make([]*pb.ImageMetadata, len(images))
	for i, img := range images {
		pbImages[i] = &pb.ImageMetadata{
			ImageId:      img.ID,
			UserId:       img.UserID,
			Filename:     img.Filename,
			ContentType:  img.ContentType,
			SizeBytes:    img.SizeBytes,
			Width:        img.Width,
			Height:       img.Height,
			UploadedAt:   timestamppb.New(img.UploadedAt),
			Url:          fmt.Sprintf("%s/images/%s", h.baseURL, img.ID),
			ThumbnailUrl: fmt.Sprintf("%s/images/%s?thumbnail=true", h.baseURL, img.ID),
		}
	}

	nextPageToken := ""
	if offset+pageSize < totalCount {
		nextPageToken = strconv.Itoa(offset + pageSize)
	}

	return &pb.ListImagesResponse{
		Images:        pbImages,
		NextPageToken: nextPageToken,
		TotalCount:    int64(totalCount),
	}, nil
}

func (h *ImageHandler) DeleteImage(ctx context.Context, req *pb.DeleteImageRequest) (*pb.DeleteImageResponse, error) {
	img, err := h.db.GetImage(ctx, req.ImageId)
	if err != nil {
		return nil, err
	}

	if img.UserID != req.UserId {
		return nil, fmt.Errorf("unauthorized")
	}

	if err := h.storage.DeleteImage(img.OriginalPath, img.ThumbnailPath); err != nil {
		log.Printf("failed to delete image files: %v", err)
	}

	if err := h.db.DeleteImage(ctx, req.ImageId); err != nil {
		return nil, err
	}

	return &pb.DeleteImageResponse{Success: true}, nil
}

func (h *ImageHandler) GetImageURL(ctx context.Context, req *pb.GetImageURLRequest) (*pb.GetImageURLResponse, error) {
	url := fmt.Sprintf("%s/images/%s", h.baseURL, req.ImageId)
	if req.Thumbnail {
		url += "?thumbnail=true"
	}
	return &pb.GetImageURLResponse{Url: url}, nil
}

func (h *ImageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	imageID := strings.TrimPrefix(r.URL.Path, "/images/")
	if imageID == "" || imageID == r.URL.Path {
		http.Error(w, "image ID required", http.StatusBadRequest)
		return
	}

	thumbnail := r.URL.Query().Get("thumbnail") == "true"

	img, err := h.db.GetImage(r.Context(), imageID)
	if err != nil {
		http.Error(w, "image not found", http.StatusNotFound)
		return
	}

	var filePath string
	var contentType string

	if thumbnail {
		filePath = img.ThumbnailPath
		contentType = "image/webp"
	} else {
		filePath = img.OriginalPath
		contentType = img.ContentType
	}

	data, err := h.storage.ReadImage(filePath)
	if err != nil {
		log.Printf("failed to read image %s: %v", filePath, err)
		http.Error(w, "failed to read image", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		log.Printf("failed to write response: %v", err)
	}
}

func (h *ImageHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprintf(w, "OK"); err != nil {
		log.Printf("failed to write health check response: %v", err)
	}
}

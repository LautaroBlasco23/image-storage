package internal

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// StorageBackend abstracts the underlying image storage mechanism.
type StorageBackend interface {
	SaveImage(userID, filename string, reader io.Reader) (imageID, originalPath, thumbnailPath string, width, height int32, size int64, err error)
	DeleteImage(originalPath, thumbnailPath string) error
	ReadImage(imagePath string) ([]byte, error)
}

type Storage struct {
	baseDir       string
	thumbnailSize uint
}

func NewStorage(baseDir string) *Storage {
	return &Storage{
		baseDir:       baseDir,
		thumbnailSize: 200,
	}
}

func (s *Storage) SaveImage(userID, filename string, reader io.Reader) (imageID, originalPath, thumbnailPath string, width, height int32, size int64, err error) {
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

	originalDir := filepath.Join(s.baseDir, "originals", userID)
	if err := os.MkdirAll(originalDir, 0o750); err != nil {
		return "", "", "", 0, 0, 0, err
	}

	originalFullPath := filepath.Join(originalDir, imageID+ext)
	if err := s.validatePath(originalFullPath); err != nil {
		return "", "", "", 0, 0, 0, err
	}
	if err := os.WriteFile(originalFullPath, data, 0o600); err != nil {
		return "", "", "", 0, 0, 0, err
	}

	thumbnailDir := filepath.Join(s.baseDir, "thumbnails", userID)
	if err := os.MkdirAll(thumbnailDir, 0o750); err != nil {
		if removeErr := os.Remove(originalFullPath); removeErr != nil {
			log.Printf("failed to remove original file on cleanup: %v", removeErr)
		}
		return "", "", "", 0, 0, 0, err
	}

	thumbData, err := generateThumbnail(img, s.thumbnailSize)
	if err != nil {
		if removeErr := os.Remove(originalFullPath); removeErr != nil {
			log.Printf("failed to remove original file on cleanup: %v", removeErr)
		}
		return "", "", "", 0, 0, 0, fmt.Errorf("failed to generate thumbnail: %w", err)
	}

	thumbnailFullPath := filepath.Join(thumbnailDir, imageID+"_thumb.webp")
	if err := s.validatePath(thumbnailFullPath); err != nil {
		if removeErr := os.Remove(originalFullPath); removeErr != nil {
			log.Printf("failed to remove original file on cleanup: %v", removeErr)
		}
		return "", "", "", 0, 0, 0, err
	}

	// #nosec G304: path is validated via validatePath()
	if err := os.WriteFile(thumbnailFullPath, thumbData, 0o600); err != nil {
		if removeErr := os.Remove(originalFullPath); removeErr != nil {
			log.Printf("failed to remove original file on cleanup: %v", removeErr)
		}
		return "", "", "", 0, 0, 0, err
	}

	originalPath = filepath.Join("originals", userID, imageID+ext)
	thumbnailPath = filepath.Join("thumbnails", userID, imageID+"_thumb.webp")
	return imageID, originalPath, thumbnailPath, width, height, size, nil
}

func (s *Storage) ReadImage(imagePath string) ([]byte, error) {
	fullPath := filepath.Join(s.baseDir, imagePath)
	if err := s.validatePath(fullPath); err != nil {
		return nil, fmt.Errorf("invalid file path: %w", err)
	}
	// #nosec G304 -- path validated above
	return os.ReadFile(fullPath)
}

func (s *Storage) DeleteImage(originalPath, thumbnailPath string) error {
	if err := s.removeFile(filepath.Join(s.baseDir, originalPath)); err != nil {
		return fmt.Errorf("failed to delete original: %w", err)
	}
	if err := s.removeFile(filepath.Join(s.baseDir, thumbnailPath)); err != nil {
		return fmt.Errorf("failed to delete thumbnail: %w", err)
	}
	return nil
}

func (s *Storage) removeFile(filePath string) error {
	if err := s.validatePath(filePath); err != nil {
		return err
	}
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Storage) validatePath(filePath string) error {
	baseDirClean := filepath.Clean(s.baseDir)
	filePathClean := filepath.Clean(filePath)
	if !strings.HasPrefix(filePathClean, baseDirClean) {
		return fmt.Errorf("path traversal detected: %s", filePath)
	}

	if len(filePathClean) > len(baseDirClean) && filePathClean[len(baseDirClean)] != filepath.Separator {
		return fmt.Errorf("path traversal detected: %s", filePath)
	}
	return nil
}

// getSafeExtension is a package-level helper used by all storage backends.
func getSafeExtension(filename, format string) string {
	if filename == "" {
		return "." + format
	}
	ext := filepath.Ext(filename)
	if ext == "" {
		return "." + format
	}
	clean := filepath.Clean(ext)
	if clean != ext || len(clean) > 5 || !strings.HasPrefix(clean, ".") {
		return "." + format
	}
	return clean
}

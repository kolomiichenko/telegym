package mock

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type inputFile struct {
	ID          string
	Filename    string
	ContentType string
	Size        int64
}

var (
	errUploadTooLarge    = errors.New("uploaded file exceeds 50 MB")
	errFileStoreTooSmall = errors.New("uploaded file exceeds file store capacity")
)

// captureUploadedFile stores a named multipart part and returns its metadata.
// It returns an empty descriptor when no matching file was uploaded.
func (h *Handlers) captureUploadedFile(c *gin.Context, kind, partName string) (inputFile, error) {
	if !strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		return inputFile{}, nil
	}
	fileHdr, err := c.FormFile(partName)
	if errors.Is(err, http.ErrMissingFile) {
		return inputFile{}, nil
	}
	if err != nil {
		return inputFile{}, err
	}
	src, err := fileHdr.Open()
	if err != nil {
		return inputFile{}, err
	}
	defer func() { _ = src.Close() }()

	const maxFile = 50 * 1024 * 1024 // 50 MB per file
	limit := min(int64(maxFile), h.Store.Files.maxBytes)
	data, err := io.ReadAll(io.LimitReader(src, limit+1))
	if err != nil {
		return inputFile{}, err
	}
	if len(data) > maxFile {
		return inputFile{}, errUploadTooLarge
	}
	// Reject before Put: an upload larger than capacity would evict the
	// existing files and itself, leaving the client with an unusable ID.
	if int64(len(data)) > h.Store.Files.maxBytes {
		return inputFile{}, errFileStoreTooSmall
	}
	contentType := fileHdr.Header.Get("Content-Type")
	id := h.Store.Files.Put(kind, contentType, fileHdr.Filename, data)
	return inputFile{
		ID: id, Filename: fileHdr.Filename,
		ContentType: contentType, Size: int64(len(data)),
	}, nil
}

func respondUploadError(c *gin.Context, err error) {
	if errors.Is(err, errUploadTooLarge) || errors.Is(err, errFileStoreTooSmall) {
		errResp(c, http.StatusRequestEntityTooLarge, "Request Entity Too Large: "+err.Error())
		return
	}
	errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
}

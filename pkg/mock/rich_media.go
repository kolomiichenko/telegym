package mock

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

// Telegram allows up to 50 media attachments per rich message, including nested blocks.
// https://core.telegram.org/bots/api#rich-message-limits
const maxRichMessageMedia = 50

type richMediaResolver struct {
	h     *Handlers
	c     *gin.Context
	count int
}

func (h *Handlers) resolveRichMedia(c *gin.Context, blocks []json.RawMessage) error {
	r := richMediaResolver{h: h, c: c}
	for i, raw := range blocks {
		var block any
		if err := json.Unmarshal(raw, &block); err != nil {
			return err
		}
		if err := r.resolve(block); err != nil {
			return err
		}
		resolved, err := json.Marshal(block)
		if err != nil {
			return err
		}
		blocks[i] = resolved
	}
	return nil
}

// Walk containers without rebuilding their text, captions or other attributes.
func (r *richMediaResolver) resolve(value any) error {
	switch value := value.(type) {
	case []any:
		for _, child := range value {
			if err := r.resolve(child); err != nil {
				return err
			}
		}
	case map[string]any:
		kind := toString(value["type"])
		switch kind {
		case "photo", "video", "animation":
			return r.resolveMedia(value, kind)
		}
		for _, child := range value {
			if err := r.resolve(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *richMediaResolver) resolveMedia(block map[string]any, kind string) error {
	media, ok := block[kind].(map[string]any)
	if !ok || toString(media["type"]) != kind {
		return fmt.Errorf("rich %s block requires %s input media", kind, kind)
	}
	reference := toString(media["media"])
	if reference == "" {
		return fmt.Errorf("media reference is required")
	}
	r.count++
	if r.count > maxRichMessageMedia {
		return fmt.Errorf("rich message can contain at most %d media", maxRichMessageMedia)
	}
	file, err := r.h.resolveInputFile(r.c, kind, reference)
	if err != nil {
		return err
	}
	block[kind] = richMediaResult(kind, file, media)
	return nil
}

func (h *Handlers) resolveInputFile(c *gin.Context, kind, reference string) (inputFile, error) {
	partName, attached := strings.CutPrefix(reference, "attach://")
	if !attached {
		return inputFile{ID: reference}, nil
	}
	if partName == "" {
		return inputFile{}, fmt.Errorf("empty attach reference")
	}
	file, err := h.captureUploadedFile(c, kind, partName)
	if err != nil {
		return inputFile{}, err
	}
	if file.ID == "" {
		return inputFile{}, fmt.Errorf("multipart attachment %q not found", partName)
	}
	return file, nil
}

func richMediaResult(kind string, file inputFile, media map[string]any) any {
	width := max(1, toInt(media["width"]))
	height := max(1, toInt(media["height"]))
	duration := 1
	if value, supplied := media["duration"]; supplied {
		duration = toInt(value)
	}
	uniqueID := file.ID + "_u"
	switch kind {
	case "photo":
		return []PhotoSize{{
			FileID: file.ID, FileUniqueID: uniqueID,
			Width: width, Height: height, FileSize: int(file.Size),
		}}
	case "video":
		return &Video{
			FileID: file.ID, FileUniqueID: uniqueID,
			Width: width, Height: height, Duration: duration,
			FileName: file.Filename, MimeType: file.ContentType, FileSize: file.Size,
		}
	default:
		return &Animation{
			FileID: file.ID, FileUniqueID: uniqueID,
			Width: width, Height: height, Duration: duration,
			FileName: file.Filename, MimeType: file.ContentType, FileSize: file.Size,
		}
	}
}

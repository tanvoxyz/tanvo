package quotes

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
)

// dataURI is a decoded data URI (lago Utils::Base64File::Decoded).
type dataURI struct {
	content     []byte
	contentType string
}

// decodeDataURI is lago Utils::Base64File.decode
// (app/services/utils/base64_file.rb): the part after the first comma,
// decoded as Ruby's Base64.decode64 does (characters outside the alphabet
// ignored, padding optional), with the content type of
// "data:<type>;base64"; false without a comma or a content type.
func decodeDataURI(s string) (dataURI, bool) {
	metadata, data, ok := strings.Cut(s, ",")
	if !ok {
		return dataURI{}, false
	}
	first, _, _ := strings.Cut(metadata, ";")
	parts := strings.Split(first, ":")
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		return dataURI{}, false
	}
	return dataURI{content: decode64(data), contentType: parts[1]}, true
}

// decode64 is Ruby's Base64.decode64 (String#unpack1("m")).
func decode64(s string) []byte {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '=' {
			break
		}
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' {
			b.WriteByte(c)
		}
	}
	clean := b.String()
	if len(clean)%4 == 1 {
		clean = clean[:len(clean)-1]
	}
	out, err := base64.RawStdEncoding.DecodeString(clean)
	if err != nil {
		return nil
	}
	return out
}

// checkUpload validates an uploaded file of field: smaller than maxSize
// bytes, of one of types (lago's active storage validations, file_too_large
// and invalid_content_type).
func checkUpload(field string, f dataURI, maxSize int, types []string) error {
	if len(f.content) >= maxSize {
		return fieldError(field, CodeFileTooLarge)
	}
	if !slices.Contains(types, f.contentType) {
		return fieldError(field, CodeInvalidContentType)
	}
	return nil
}

// Image is an uploaded quote image.
type Image struct {
	// ID identifies the image.
	ID string
	// URL downloads it; empty without a blob store.
	URL string
}

// AddImage uploads image, a base64 data URI, to quote id and returns it
// (lago Quotes::AddImageService, app/services/quotes/add_image_service.rb):
// image invalid_format when it is not a data URI, file_too_large from 5 MB
// and invalid_content_type unless PNG, JPEG, WebP or GIF.
func (s *Service) AddImage(ctx context.Context, org core.OrganizationID, id core.QuoteID, image string) (*Image, error) {
	var out *Image
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		q, err := r.Quotes.Get(ctx, org, id)
		if err != nil {
			return notFound(err, "quote")
		}
		f, ok := decodeDataURI(image)
		if !ok {
			return fieldError("image", core.CodeInvalidFormat)
		}
		if err := checkUpload("image", f, quote.ImageMaxSize, quote.ImageContentTypes); err != nil {
			return err
		}
		if s.blobs == nil {
			return core.NewRuleError(CodeBlobStoreNotConfigured, "images cannot be stored: no blob store is configured")
		}
		_, subtype, _ := strings.Cut(f.contentType, "/")
		ref, err := s.blobs.Put(ctx, org, "quote-image-"+uuid.NewString()+"."+subtype, f.contentType, bytes.NewReader(f.content))
		if err != nil {
			return fmt.Errorf("store image: %w", err)
		}
		q.Images = append(q.Images, ref)
		q.UpdatedAt = s.d.Now()
		if err := r.Quotes.Update(ctx, q); err != nil {
			return fmt.Errorf("update quote %s: %w", id, err)
		}
		u, err := s.blobURL(ctx, org, ref)
		if err != nil {
			return err
		}
		out = &Image{ID: string(ref), URL: u}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("add image to quote %s: %w", id, err)
	}
	return out, nil
}

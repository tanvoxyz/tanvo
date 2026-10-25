package app

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/documents/blob"
)

// Paths of the blob handlers under TANVO_API_URL: the permanent file
// links (lago rails_blob_url, which redirects to the service URL) and the
// short-lived download URLs of the local backend.
const (
	fileLinksPath = "/files/"
	localBlobPath = "/blobs/"
)

// Purposes of the keys of the blob store derived from TANVO_SECRET_KEY.
const (
	purposeBlobLinks = "tanvo/blob-links/v1"
	purposeBlobLocal = "tanvo/blob-local/v1"
)

// gcsEndpoint is the S3 interoperability endpoint of Google Cloud Storage.
const gcsEndpoint = "https://storage.googleapis.com"

// blobs is the configured blob store: the backend wrapped with permanent
// links, and the HTTP handlers serving them.
type blobs struct {
	// store is nil when no backend is configured.
	store  port.BlobStore
	links  http.Handler
	local  http.Handler
	closer io.Closer
}

// newBlobs builds the blob store of TANVO_BLOB_BACKEND; an empty backend
// returns a [blobs] without a store.
func newBlobs(cfg config.Config, log *slog.Logger, tp trace.TracerProvider) (*blobs, error) {
	if cfg.BlobBackend == "" {
		return &blobs{}, nil
	}
	secret, err := cfg.SecretKeyBytes()
	if err != nil {
		return nil, err
	}
	if secret == nil || cfg.APIURL == "" {
		return nil, errors.New("blob store: TANVO_SECRET_KEY and TANVO_API_URL are required")
	}
	api := strings.TrimSuffix(cfg.APIURL, "/")
	b := &blobs{}
	var backend port.BlobStore
	switch cfg.BlobBackend {
	case config.BlobLocal:
		key, err := deriveKey(secret, purposeBlobLocal)
		if err != nil {
			return nil, err
		}
		l, err := blob.NewLocal(blob.LocalOptions{Root: cfg.BlobLocalRoot, BaseURL: api + localBlobPath, Key: key, TTL: cfg.BlobURLTTL, Logger: log})
		if err != nil {
			return nil, fmt.Errorf("blob store: %w", err)
		}
		backend, b.local, b.closer = l, l, l
	case config.BlobS3, config.BlobGCS:
		endpoint, region := cfg.BlobEndpoint, cfg.BlobRegion
		if cfg.BlobBackend == config.BlobGCS {
			if endpoint == "" {
				endpoint = gcsEndpoint
			}
			if region == "" {
				region = "auto"
			}
		}
		s, err := blob.NewS3(blob.S3Options{
			Endpoint: endpoint, Region: region, Bucket: cfg.BlobBucket,
			AccessKeyID: cfg.BlobAccessKeyID, SecretAccessKey: cfg.BlobSecretAccessKey, SessionToken: cfg.BlobSessionToken,
			PathStyle: cfg.BlobPathStyle, Prefix: cfg.BlobPrefix, URLExpiry: cfg.BlobURLTTL,
			HTTPClient: &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport, otelhttp.WithTracerProvider(tp))},
		})
		if err != nil {
			return nil, fmt.Errorf("blob store: %w", err)
		}
		backend = s
	default:
		return nil, fmt.Errorf("blob store: unknown backend %q", cfg.BlobBackend)
	}
	key, err := deriveKey(secret, purposeBlobLinks)
	if err != nil {
		return nil, err
	}
	links, err := blob.NewLinks(backend, blob.LinksOptions{BaseURL: api + fileLinksPath, Key: key, Logger: log})
	if err != nil {
		return nil, fmt.Errorf("blob store: %w", err)
	}
	b.store, b.links = links, links
	return b, nil
}

// mount serves the file links and the local downloads on mux.
func (b *blobs) mount(mux *http.ServeMux) {
	if b.links != nil {
		mux.Handle(fileLinksPath, http.StripPrefix(strings.TrimSuffix(fileLinksPath, "/"), b.links))
	}
	if b.local != nil {
		mux.Handle(localBlobPath, http.StripPrefix(strings.TrimSuffix(localBlobPath, "/"), b.local))
	}
}

// close releases the backend.
func (b *blobs) close() error {
	if b.closer == nil {
		return nil
	}
	return b.closer.Close()
}

// blobOrNil returns the store as a port, nil (not a typed nil) when none
// is configured.
func (b *blobs) blobOrNil() port.BlobStore {
	if b == nil || b.store == nil {
		return nil
	}
	return b.store
}

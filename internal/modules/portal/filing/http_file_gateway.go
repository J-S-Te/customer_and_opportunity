package filing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
)

// HTTPFileGatewayStore keeps the Portal business ACL in Portal while all bytes are written to the
// independent gateway. The object key is an opaque encrypted Portal reference, never a disk path.
type HTTPFileGatewayStore struct{ client *filegatewayclient.Client }

func NewHTTPFileGatewayStore(client *filegatewayclient.Client) *HTTPFileGatewayStore {
	if client == nil {
		return nil
	}
	return &HTTPFileGatewayStore{client: client}
}

func (s *HTTPFileGatewayStore) Available() bool { return s != nil && s.client != nil }

func portalGatewayInput(key, media string, size uint64, digest, name string) (filegatewayclient.V2UploadInput, error) {
	parts := strings.Split(strings.Trim(key, "/"), "/")
	if len(parts) != 5 || parts[0] != "portal" || parts[1] != "filings" || parts[2] == "" || parts[3] == "" || parts[4] == "" {
		return filegatewayclient.V2UploadInput{}, errors.New("invalid portal material reference")
	}
	sum := sha256.Sum256([]byte(key))
	return filegatewayclient.V2UploadInput{
		RequestID: "portal-filing-material-" + hex.EncodeToString(sum[:16]), Purpose: "portal.filing.material", Classification: "CONFIDENTIAL",
		Name: name, MediaType: media, SizeBytes: size, SHA256: strings.ToLower(strings.TrimSpace(digest)),
		ResourceType: "PORTAL_FILING_MATERIAL", ResourceID: parts[len(parts)-1], BindingType: "FILING_MATERIAL", DisplayName: name,
	}, nil
}

func (s *HTTPFileGatewayStore) CreateUpload(ctx context.Context, key, media string, size uint64, digest, name string) (ObjectUploadGrant, error) {
	input, err := portalGatewayInput(key, media, size, digest, name)
	if err != nil {
		return ObjectUploadGrant{}, err
	}
	grant, err := s.client.CreateV2DirectUpload(ctx, input)
	if err != nil || grant.Status != "CREATED" || grant.Ticket == "" || grant.UploadURL == "" {
		return ObjectUploadGrant{}, ErrMaterialUnavailable
	}
	return ObjectUploadGrant{URL: grant.UploadURL, Ticket: grant.Ticket, ExpiresAt: grant.ExpiresAt}, nil
}

func (s *HTTPFileGatewayStore) Finalize(ctx context.Context, key, media string, size uint64, digest, name string) (MaterialObjectMetadata, error) {
	input, err := portalGatewayInput(key, media, size, digest, name)
	if err != nil {
		return MaterialObjectMetadata{}, err
	}
	grant, err := s.client.ResolveV2Upload(ctx, input)
	if err != nil || grant.Status != "READY" || grant.FileID == "" {
		return MaterialObjectMetadata{}, ErrMaterialNotReady
	}
	return MaterialObjectMetadata{ObjectVersion: grant.FileID, MIMEType: media, SizeBytes: size, SHA256: strings.ToLower(digest)}, nil
}

func (s *HTTPFileGatewayStore) OpenVerified(ctx context.Context, key, version, _ string, _ uint64) (io.ReadCloser, error) {
	input, err := portalGatewayInput(key, "application/pdf", 1, strings.Repeat("0", 64), "material")
	if err != nil {
		return nil, err
	}
	return s.client.OpenV2File(ctx, input.RequestID+"-download", version, input.ResourceType, input.ResourceID)
}

var _ MaterialObjectStore = (*HTTPFileGatewayStore)(nil)

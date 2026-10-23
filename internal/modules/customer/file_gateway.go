package customer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
)

type ImportGatewayAdapter struct {
	client        *filegatewayclient.Client
	applicationID string
}

func NewImportGatewayAdapter(client *filegatewayclient.Client, applicationID ...string) *ImportGatewayAdapter {
	if client == nil {
		return nil
	}
	adapter := &ImportGatewayAdapter{client: client}
	if len(applicationID) > 0 {
		adapter.applicationID = applicationID[0]
	}
	return adapter
}

func (a *ImportGatewayAdapter) StoreImport(ctx context.Context, jobNo, actorUserID, filename string, content []byte) error {
	_, err := a.client.UploadV2(ctx, filegatewayclient.V2UploadInput{
		RequestID: "crm-customer-import-" + jobNo, Purpose: "crm.customer.import", Classification: "INTERNAL",
		ActorUserID: actorUserID, Name: filename,
		MediaType:    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		ResourceType: "CUSTOMER_IMPORT", ResourceID: jobNo, BindingType: "SOURCE", DisplayName: filename,
		Content: bytes.NewReader(content),
	})
	return err
}

var _ ImportFileGateway = (*ImportGatewayAdapter)(nil)

func importSourceUploadInput(source *ImportSource, content []byte) filegatewayclient.V2UploadInput {
	digest := sha256.Sum256(content)
	return filegatewayclient.V2UploadInput{RequestID: source.RequestID, Purpose: "crm.customer.import", Classification: "INTERNAL",
		ActorUserID: source.ActorID, Name: source.Filename,
		MediaType:    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		ResourceType: "CUSTOMER_IMPORT", ResourceID: source.JobNo, BindingType: "SOURCE", DisplayName: source.Filename,
		Content: bytes.NewReader(content), SizeBytes: uint64(len(content)), SHA256: hex.EncodeToString(digest[:])}
}

func (a *ImportGatewayAdapter) UploadImport(ctx context.Context, source *ImportSource, content []byte) (string, error) {
	receipt, err := a.client.UploadV2(ctx, importSourceUploadInput(source, content))
	if err != nil {
		return "", err
	}
	return receipt.FileID, nil
}

func (a *ImportGatewayAdapter) ResolveImport(ctx context.Context, source *ImportSource, content []byte) (string, error) {
	grant, err := a.client.ResolveV2Upload(ctx, importSourceUploadInput(source, content))
	if err != nil {
		return "", err
	}
	return grant.FileID, nil
}

func (a *ImportGatewayAdapter) ReleaseImport(ctx context.Context, source *ImportSource) error {
	if a.applicationID == "" {
		return errors.New("customer import gateway application is required")
	}
	return a.client.ReleaseBinding(ctx, source.RequestID+"-release", a.applicationID, source.FileID, "CUSTOMER_IMPORT", source.JobNo, "SOURCE")
}

var _ DurableImportFileGateway = (*ImportGatewayAdapter)(nil)

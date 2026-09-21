package customer

import (
	"bytes"
	"context"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/filegatewayclient"
)

type ImportGatewayAdapter struct{ client *filegatewayclient.Client }

func NewImportGatewayAdapter(client *filegatewayclient.Client) *ImportGatewayAdapter {
	if client == nil {
		return nil
	}
	return &ImportGatewayAdapter{client: client}
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

package opportunity

import "testing"

func TestGatewayFileStatusPreservesLegacyAttachmentCompatibility(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		AttachmentPendingUpload: "PENDING_UPLOAD",
		AttachmentFinalizing:    "VALIDATING",
		AttachmentScanning:      "VALIDATING",
		AttachmentClean:         "READY",
		AttachmentRejected:      "REJECTED",
		AttachmentScanFailed:    "FAILED",
	}
	for legacy, expected := range tests {
		if actual := gatewayFileStatus(legacy); actual != expected {
			t.Fatalf("gatewayFileStatus(%q) = %q, want %q", legacy, actual, expected)
		}
	}
}

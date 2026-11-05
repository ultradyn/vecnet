package hub

import "testing"

// The storage switch cannot affect either existing public wire renderer.
func TestDeliveryReferenceRendererGuards(t *testing.T) {
	t.Run("reference_storage/TestReferenceGuardWireRenderer", TestReferenceGuardWireRenderer)
	t.Run("reference_storage/TestReferenceGuardBlobURLRenderer", TestReferenceGuardBlobURLRenderer)
}

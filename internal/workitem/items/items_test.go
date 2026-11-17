package items

import "testing"

// A ticket names the run's branch before a merge request puts its checkout
// on its own: registration order is preparation order. Nothing, which
// prepares none, comes last.
func TestAllRegistersEveryKindInOrder(t *testing.T) {
	ks := All().Kinds()
	if len(ks) != 3 || ks[0].Name() != "ticket" || ks[1].Name() != "merge_request" || ks[2].Name() != "nothing" {
		t.Errorf("kinds = %v", ks)
	}
}

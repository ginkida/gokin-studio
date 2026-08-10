//go:build windows

package studio

import "testing"

func TestQuickEntryAcceptedKeysHaveWindowsMappings(t *testing.T) {
	for _, key := range quickEntryKeyNames() {
		if _, ok := windowsQuickEntryVirtualKey(key); !ok {
			t.Errorf("accepted key %s has no Windows virtual key", key)
		}
	}
}

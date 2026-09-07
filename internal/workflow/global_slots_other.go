//go:build !darwin && !linux

package workflow

import "context"

type globalSlot struct{}

func (globalSlot) Release() {}

// Other platforms retain the legacy scheduler until an OS-specific crash-safe
// lock implementation is available.
func acquireGlobalSlot(ctx context.Context, profile, pool string, cap int) (globalSlot, error) {
	if cap <= 0 {
		return globalSlot{}, nil
	}
	select {
	case <-ctx.Done():
		return globalSlot{}, ctx.Err()
	default:
		return globalSlot{}, nil
	}
}

func tryAcquireGlobalSlot(profile, pool string, cap int) (globalSlot, bool, error) {
	if cap <= 0 {
		return globalSlot{}, true, nil
	}
	return globalSlot{}, true, nil
}

func profileSlotIdentity(profileName, profileDigest string) string {
	if profileDigest != "" {
		return profileDigest
	}
	if profileName != "" {
		return profileName
	}
	return "workflow-v1"
}

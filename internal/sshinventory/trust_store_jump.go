package sshinventory

import (
	"crypto/sha256"
	"crypto/subtle"
	"path/filepath"
	"sort"
)

// EnrollSingleJumpPin enrolls one confirmed pin only for a current supported
// one-hop route. Call it separately for the destination and gateway; until
// both records exist, PrepareEnrolledSingleJumpProbe refuses the connection.
func (s *DirectTrustStore) EnrollSingleJumpPin(userConfig, systemConfig string, target, jump, selected Selection, confirmed ConfirmedDirectHostKey) error {
	if err := validateSingleJumpRoute(userConfig, systemConfig, target, jump); err != nil {
		return err
	}
	if selected.Alias != target.Alias && selected.Alias != jump.Alias {
		return ErrProbeUnsupported
	}
	return s.Enroll(userConfig, systemConfig, selected, confirmed)
}

// PrepareEnrolledSingleJumpProbe reads separate approved pins for the selected
// destination and gateway. The two records remain locked while the diagnostic
// executes so a concurrent rotation cannot change either approval mid-probe.
// Enroll each key only after its own independent fingerprint review and user
// confirmation. This method does not install keys or run a remote command.
func (s *DirectTrustStore) PrepareEnrolledSingleJumpProbe(userConfig, systemConfig string, target, jump Selection) (*ProbePlan, error) {
	if err := validateSingleJumpRoute(userConfig, systemConfig, target, jump); err != nil {
		return nil, err
	}
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	targetPath, targetHost, err := s.fileFor(target)
	if err != nil {
		return nil, err
	}
	jumpPath, jumpHost, err := s.fileFor(jump)
	if err != nil {
		return nil, err
	}
	targetName, jumpName := filepath.Base(targetPath), filepath.Base(jumpPath)
	if targetName == jumpName {
		return nil, ErrProbeUnsupported
	}
	targetLine, err := s.read(root, targetName, targetHost)
	if err != nil {
		return nil, err
	}
	jumpLine, err := s.read(root, jumpName, jumpHost)
	if err != nil {
		return nil, err
	}
	plan, err := PrepareSingleJumpProbe(userConfig, systemConfig, target, jump, append(targetLine, jumpLine...))
	if err != nil {
		return nil, err
	}
	targetDigest, jumpDigest := sha256.Sum256(targetLine), sha256.Sum256(jumpLine)
	plan.trustCheck = func() error {
		currentRoot, err := s.openRoot()
		if err != nil {
			return err
		}
		defer func() { _ = currentRoot.Close() }()
		for _, pin := range []struct {
			name, host string
			digest     [sha256.Size]byte
		}{{targetName, targetHost, targetDigest}, {jumpName, jumpHost, jumpDigest}} {
			current, err := s.read(currentRoot, pin.name, pin.host)
			if err != nil {
				return err
			}
			actual := sha256.Sum256(current)
			if subtle.ConstantTimeCompare(actual[:], pin.digest[:]) != 1 {
				return ErrChanged
			}
		}
		return nil
	}
	plan.trustLock = func() (func(), error) {
		currentRoot, err := s.openRoot()
		if err != nil {
			return nil, err
		}
		names := []string{targetName + ".lock", jumpName + ".lock"}
		sort.Strings(names)
		firstUnlock, err := lockPrivateTrustRecord(currentRoot, names[0])
		if err != nil {
			_ = currentRoot.Close()
			return nil, err
		}
		secondUnlock, err := lockPrivateTrustRecord(currentRoot, names[1])
		if err != nil {
			firstUnlock()
			_ = currentRoot.Close()
			return nil, err
		}
		return func() {
			secondUnlock()
			firstUnlock()
			_ = currentRoot.Close()
		}, nil
	}
	if err := plan.Revalidate(); err != nil {
		_ = plan.Close()
		return nil, err
	}
	return plan, nil
}

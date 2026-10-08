//go:build windows

package sshinventory

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func currentWindowsUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

func createPrivateTrustDirectory(path string) error {
	sid, err := currentWindowsUserSID()
	if err != nil {
		return err
	}
	// Protect the DACL at creation time; children inherit only this account
	// and LocalSystem. The caller must supply a trusted parent directory.
	sd, err := windows.SecurityDescriptorFromString("O:" + sid.String() + "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + sid.String() + ")")
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(name, sa); err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return os.ErrExist
		}
		return err
	}
	return nil
}

func preparePrivateTrustFile(path string, _ *os.File) error {
	// Go's ordinary file creation inherits the private DACL, but an elevated
	// Windows token can select Administrators as its default owner. Assign the
	// current account before storing bytes and verify the opened handle next.
	sid, err := currentWindowsUserSID()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION, sid, nil, nil, nil)
}

func privateTrustDirectory(path string, info os.FileInfo) bool {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var opened windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &opened); err != nil || opened.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	return privateWindowsSecurity(handle, true)
}

func privateTrustFile(info os.FileInfo, file *os.File) bool {
	return info != nil && info.Mode().IsRegular() && file != nil &&
		privateWindowsSecurity(windows.Handle(file.Fd()), false)
}

func privateWindowsSecurity(handle windows.Handle, requireProtected bool) bool {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil || !sd.IsValid() {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return false
	}
	user, err := currentWindowsUserSID()
	if err != nil || !owner.Equals(user) {
		return false
	}
	control, _, err := sd.Control()
	if err != nil || (requireProtected && control&windows.SE_DACL_PROTECTED == 0) {
		return false
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount == 0 {
		return false
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return false
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false
		}
		entrySID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !entrySID.IsValid() || (!entrySID.Equals(user) && !entrySID.Equals(system)) {
			return false
		}
	}
	return true
}

func openPrivatePin(root *os.Root, name string) (*os.File, error) {
	// Root keeps the open inside the anchored directory. The caller compares
	// the opened file with Lstat and validates its ACL before reading bytes.
	return root.OpenFile(name, os.O_RDONLY, 0)
}

func lockPrivateTrustRecord(root *os.Root, name string) (func(), error) {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	created := err == nil
	if os.IsExist(err) {
		file, err = root.OpenFile(name, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, err
	}
	if created {
		if err := preparePrivateTrustFile(filepath.Join(root.Name(), name), file); err != nil {
			_ = file.Close()
			_ = root.Remove(name)
			return nil, err
		}
	}
	info, statErr := root.Lstat(name)
	opened, openErr := file.Stat()
	if statErr != nil || openErr != nil || !privateTrustFile(opened, file) || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, ErrProbeUnsupported
	}
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped); err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrTrustBusy
		}
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
		_ = file.Close()
	}, nil
}

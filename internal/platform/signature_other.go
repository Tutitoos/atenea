//go:build !darwin

package platform

// SelfSignedStably is a macOS question. Nothing else binds a permission to a
// code signature the way TCC does, so there is nothing here to be unstable.
func SelfSignedStably() (bool, string) { return true, "" }

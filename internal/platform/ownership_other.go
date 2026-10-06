//go:build !linux && !darwin

package platform

import "os"

func rootOwned(os.FileInfo) bool { return false }

func ownedByCurrentUser(os.FileInfo) bool { return false }

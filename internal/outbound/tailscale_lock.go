package outbound

import (
	"errors"
	"os"
	"rillway/internal/config"
	"syscall"
)

// Keep the descriptor open for the entire node lifetime. The kernel releases
// the lock on process exit, so a stale lock file does not strand the next start.
func lockTailscaleState(dir string) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.OpenFile(".rillway.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, config.PublicError{Message: "The Tailscale state lock must be a private regular file."}
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, config.PublicError{Message: "The Tailscale state directory is already in use by another Rillway process. Use a separate state directory.", Err: err}
		}
		return nil, config.PublicError{Message: "Could not lock the Tailscale state directory.", Err: err}
	}
	return file, nil
}

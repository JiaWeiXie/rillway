//go:build !linux

package memorylimit

import (
	"context"
)

func ServeHelper(context.Context, string) error {
	return messageError("Service memory limits require a Linux systemd installation.")
}

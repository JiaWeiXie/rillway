//go:build !linux

package resourcecontrol

import (
	"context"
)

func ServeHelper(context.Context, string) error {
	return messageError("Service resource control requires a Linux systemd installation.")
}

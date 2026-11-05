//go:build !tray

package main

import "context"

// runUI runs the bridge in the foreground. Builds made with -tags tray use the
// system tray version in tray.go instead.
func runUI(ctx context.Context, _ context.CancelFunc, run func(context.Context) error) error {
	return run(ctx)
}

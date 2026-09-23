//go:build !windows

package main

import (
	"context"
	"fmt"
)

func (a *App) startAutoUpdater(context.Context) {}
func (a *App) finishAutoUpdate()                {}
func applyUpdate([]string) int                  { return 1 }
func consumeUpdateError() string                { return "" }
func (a *App) downloadAvailableUpdate() error {
	return fmt.Errorf("updates are available only on Windows")
}

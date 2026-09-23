//go:build !windows

package main

import "context"

func (a *App) startAutoUpdater(context.Context) {}
func (a *App) finishAutoUpdate()                {}
func applyUpdate([]string) int                  { return 1 }
func consumeUpdateError() string                { return "" }

//go:build !frigo_test

package testsync

import "context"

// Point is inert unless the frigo_test build tag is enabled.
func Point(context.Context, string) error { return nil }

// Notify is inert unless the frigo_test build tag is enabled.
func Notify(string) error { return nil }

// Fail is inert unless the frigo_test build tag is enabled.
func Fail(string) error { return nil }

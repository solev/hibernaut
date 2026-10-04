package main

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestBrowserOpenerUsesDesktopCommandWithoutStdio(t *testing.T) {
	for _, tc := range []struct {
		name, platform, display, wayland, command string
	}{
		{"mac", "darwin", "", "", "open"},
		{"linux display", "linux", ":0", "", "xdg-open"},
		{"linux wayland", "linux", "", "wayland-0", "xdg-open"},
		{"headless linux", "linux", "", "", ""},
		{"unsupported", "windows", ":0", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(key string) string {
				switch key {
				case "DISPLAY":
					return tc.display
				case "WAYLAND_DISPLAY":
					return tc.wayland
				}
				return ""
			}
			var commands, runs int
			opener := browserOpener(tc.platform, getenv, func(program string) runFunc {
				commands++
				if program != tc.command {
					t.Errorf("browser command = %q, want %q", program, tc.command)
				}
				return func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
					runs++
					if !slices.Equal(args, []string{"https://github.com/login/device"}) {
						t.Error("the opener didn't receive just the device URL")
					}
					if stdin != nil || stdout != nil || stderr != nil {
						t.Error("the opener inherited stdio")
					}
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) <= 9*time.Second || time.Until(deadline) > 10*time.Second {
						t.Error("the opener didn't receive its 10 second timeout")
					}
					return 0, nil
				}
			})
			if tc.command == "" {
				if opener != nil || commands != 0 {
					t.Error("a browser opener was set without a desktop command")
				}
				return
			}
			if err := opener(t.Context(), hibapi.GitHubDeviceURL); err != nil {
				t.Fatal(err)
			}
			if commands != 1 || runs != 1 {
				t.Error("the desktop opener didn't run once")
			}
		})
	}
}

func TestBrowserOpenerReportsCommandFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		err  error
		want string
	}{
		{"missing", -1, errors.New("open not found"), "open not found"},
		{"exit", 1, nil, "open exited 1"},
		{"timeout", -1, context.DeadlineExceeded, "context deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opener := browserOpener("darwin", func(string) string { return "" }, func(string) runFunc {
				return func(context.Context, []string, io.Reader, io.Writer, io.Writer) (int, error) {
					return tc.code, tc.err
				}

			})
			err := opener(t.Context(), hibapi.GitHubDeviceURL)
			if err == nil || err.Error() != tc.want {
				t.Errorf("browser failure = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestBrowserOpenerStopsOnTimeoutOrLoginCancellation(t *testing.T) {
	for _, cancelLogin := range []bool{false, true} {
		t.Run("cancel login "+strconv.FormatBool(cancelLogin), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				opener := browserOpener("darwin", func(string) string { return "" }, func(string) runFunc {
					return func(ctx context.Context, _ []string, _ io.Reader, _, _ io.Writer) (int, error) {
						if cancelLogin {
							cancel()
						}
						<-ctx.Done()
						return -1, ctx.Err()
					}
				})
				start := time.Now()
				err := opener(ctx, hibapi.GitHubDeviceURL)
				wantErr, wantElapsed := context.DeadlineExceeded, 10*time.Second
				if cancelLogin {
					wantErr, wantElapsed = context.Canceled, 0
				}
				if !errors.Is(err, wantErr) || time.Since(start) != wantElapsed {
					t.Errorf("opener stopped after %v with %v, want %v and %v", time.Since(start), err, wantElapsed, wantErr)
				}
			})
		})
	}
}

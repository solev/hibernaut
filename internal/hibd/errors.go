package hibd

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
)

func withNextStep(cmd string, err error) error {
	message := err.Error()
	var next string
	switch {
	case errors.Is(err, room.ErrFull):
		if strings.Contains(message, "can't fit on this host") {
			next = "Use a smaller size where supported, or move Hibernaut to a larger host"
		} else {
			next = "Run hib ls, then pause or remove machines you don't need before trying again"
		}
	case errors.Is(err, engine.ErrNotRunning):
		next = "Wait for pausing or saving to finish, then check hib ls before trying again"
	case errors.Is(err, engine.ErrNoTemplate) && !strings.Contains(message, "hib image build"):
		next = "Run hib image ls to check image names before trying again"
	case strings.HasPrefix(message, "locking vault:"):
		next = "Finish the command holding the vault lock, then try again"
	case strings.Contains(message, "vault key"):
		next = "On the host, check the vault key and its root-only permissions, then run hib status"
	default:
		if usesE2B(cmd) {
			var network *url.Error
			if errors.As(err, &network) || strings.Contains(strings.ToLower(message), "e2b unreachable") {
				next = "Check E2B on the host, then run hib status before trying again"
			}
		}
	}
	if next == "" {
		return err
	}
	separator := ". "
	if strings.HasSuffix(message, ".") {
		separator = " "
	}
	return fmt.Errorf("%w%s%s", err, separator, next)
}

func usesE2B(cmd string) bool {
	switch cmd {
	case "new", "ls", "rm", "pause", "fork", "bridge", "image build", "image save", "image ls", "image rm",
		"ready", "cleanup", "leakscan", "pause-all", "status":
		return true
	}
	return false
}

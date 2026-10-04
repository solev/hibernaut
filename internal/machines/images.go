package machines

import (
	"context"
	"errors"

	"github.com/solev/hibernaut/internal/hibapi"
)

// SaveImage saves machine as image name, which machines can then be made from, and returns the
// image. name follows hibapi.CheckName, and can't be a built-in image's or an existing saved
// image's. So that a failed save costs machine at most what it did since the save began,
// machine is paused, its snapshot confirmed with Config.Durable, and resumed before the save.
// It's left running or paused as it was.
func (m *Manager) SaveImage(ctx context.Context, machine, name string) (hibapi.Image, error) {
	return hibapi.Image{}, errors.ErrUnsupported
}

// Images returns the built-in images, built or not, then the saved ones, each sorted by name.
func (m *Manager) Images(ctx context.Context) ([]hibapi.Image, error) {
	return nil, errors.ErrUnsupported
}

// RemoveImage deletes saved image name, never a built-in one. While machines use the image, it
// fails and names them.
func (m *Manager) RemoveImage(ctx context.Context, name string) error {
	return errors.ErrUnsupported
}

//go:build linux

package cleanup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type localTrimStorage struct {
	fd    int
	punch func(int, int64, int64) error
}

// OpenTrimStorage opens local snapshot storage without following its final symlink.
func OpenTrimStorage(path string) (TrimStorage, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &localTrimStorage{fd: fd, punch: func(fd int, start, length int64) error {
		return unix.Fallocate(fd, unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, start, length)
	}}, nil
}

func (s *localTrimStorage) Close() error { return unix.Close(s.fd) }

func (s *localTrimStorage) directory(build string) (int, error) {
	if !buildName.MatchString(build) {
		return -1, fmt.Errorf("invalid build directory %q", build)
	}
	return unix.Openat(s.fd, build, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

func (s *localTrimStorage) Directory(build string) error {
	fd, err := s.directory(build)
	if err == nil {
		err = unix.Close(fd)
	}
	return err
}

func (s *localTrimStorage) open(key FileKey, writable bool) (int, error) {
	switch key.Name {
	case "memfile", "rootfs.ext4", "memfile.header", "rootfs.ext4.header":
	default:
		return -1, fmt.Errorf("invalid snapshot file %q", key.Name)
	}
	dir, err := s.directory(key.Build)
	if err != nil {
		return -1, err
	}
	defer unix.Close(dir)
	mode := unix.O_RDONLY
	if writable {
		mode = unix.O_RDWR
	}
	return unix.Openat(dir, key.Name, mode|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOATIME, 0)
}

func (s *localTrimStorage) Header(key FileKey) ([]byte, error) {
	fd, err := s.open(key, false)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), key.Name)
	defer f.Close()
	st, err := fileState(fd)
	if err != nil {
		return nil, err
	}
	if !st.Regular {
		return nil, errors.New("not a regular file")
	}
	return io.ReadAll(f)
}

func (s *localTrimStorage) Stat(key FileKey) (FileState, error) {
	fd, err := s.open(key, false)
	if err != nil {
		return FileState{}, err
	}
	defer unix.Close(fd)
	return fileState(fd)
}

func fileState(fd int) (FileState, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return FileState{}, err
	}
	return FileState{
		Device: uint64(st.Dev), Inode: st.Ino, Links: uint64(st.Nlink), Size: st.Size,
		Blocks: st.Blocks, Modified: time.Unix(st.Mtim.Sec, st.Mtim.Nsec),
		Regular: st.Mode&unix.S_IFMT == unix.S_IFREG,
	}, nil
}

func ext4BlockSize(fd int) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(fd, &st); err != nil {
		return 0, err
	}
	if st.Type != 0xEF53 {
		return 0, fmt.Errorf("storage isn't on ext4 (filesystem type %#x)", st.Type)
	}
	if st.Bsize <= 0 {
		return 0, fmt.Errorf("invalid filesystem block size %d", st.Bsize)
	}
	return st.Bsize, nil
}

func (s *localTrimStorage) BlockSize() (int64, error) { return ext4BlockSize(s.fd) }

func (s *localTrimStorage) Apply(ctx context.Context, plan FilePlan, dry bool) (result TrimResult, err error) {
	fd, err := s.open(plan.File.Key, !dry)
	if err != nil {
		return TrimResult{Left: err.Error()}, nil
	}
	defer unix.Close(fd)
	before, err := fileState(fd)
	if err != nil {
		return TrimResult{Left: err.Error()}, nil
	}
	if reason := identityChanged(before, plan.File.State); reason != "" {
		return TrimResult{Left: reason}, nil
	}
	block, err := ext4BlockSize(fd)
	if err != nil {
		return TrimResult{Left: err.Error()}, nil
	}
	punched := false
	defer func() {
		if dry {
			return
		}
		if punched {
			ts := []unix.Timespec{{Nsec: unix.UTIME_OMIT}, {Sec: before.Modified.Unix(), Nsec: int64(before.Modified.Nanosecond())}}
			if restore := unix.UtimesNanoAt(fd, "", ts, unix.AT_EMPTY_PATH); restore != nil {
				result.Left = "restoring mtime: " + restore.Error()
				err = errors.Join(err, fmt.Errorf("restoring %s %s mtime: %w", plan.File.Key.Build, plan.File.Key.Name, restore))
			}
		}
		after, statErr := fileState(fd)
		if statErr != nil {
			err = errors.Join(err, statErr)
		} else {
			result.Freed = max(int64(0), (before.Blocks-after.Blocks)*512)
		}
	}()
	for _, hole := range plan.Holes {
		if hole.Start < 0 || hole.End > before.Size || hole.Start >= hole.End || hole.Start%block != 0 || hole.End%block != 0 {
			return TrimResult{Left: "hole isn't within the file and filesystem block aligned"}, nil
		}
		pos := hole.Start
		for pos < hole.End {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			data, seekErr := unix.Seek(fd, pos, unix.SEEK_DATA)
			if errors.Is(seekErr, unix.ENXIO) {
				break
			}
			if seekErr != nil {
				result.Left = "finding data extents: " + seekErr.Error()
				return result, nil
			}
			if data >= hole.End {
				break
			}
			end, seekErr := unix.Seek(fd, data, unix.SEEK_HOLE)
			if seekErr != nil || end <= data {
				result.Left = fmt.Sprintf("finding hole extents: %v", seekErr)
				return result, nil
			}
			end = min(end, hole.End)
			if dry {
				result.Freed += end - data
			} else {
				if err := ctx.Err(); err != nil {
					return result, err
				}
				if err := s.punch(fd, data, end-data); err != nil {
					result.Left = "punching a hole: " + err.Error()
					return result, nil
				}
				punched = true
			}
			pos = end
		}
	}
	return result, nil
}

func identityChanged(current, planned FileState) string {
	var reasons []string
	if !current.Regular {
		reasons = append(reasons, "not a regular file")
	}
	if current.Links != 1 {
		reasons = append(reasons, "it doesn't have exactly one link")
	}
	if current.Device != planned.Device || current.Inode != planned.Inode {
		reasons = append(reasons, "it was replaced after the plan")
	}
	if current.Size != planned.Size {
		reasons = append(reasons, "its size changed after the plan")
	}
	if !current.Modified.Equal(planned.Modified) {
		reasons = append(reasons, "its mtime changed after the plan")
	}
	return strings.Join(reasons, ". ")
}

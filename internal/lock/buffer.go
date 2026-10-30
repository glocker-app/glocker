package lock

import (
	wlos "github.com/neurlang/wayland/os"
	"github.com/neurlang/wayland/wl"
)

// shmBuffer is one frame buffer shared with the compositor. It is busy from the
// commit that attaches it until the compositor's release event. Shared by both
// Wayland lock backends (ext-session-lock and wlr-layer-shell).
type shmBuffer struct {
	buf  *wl.Buffer
	data []byte // mmap of the buffer's memory
	busy bool
}

func (b *shmBuffer) HandleBufferRelease(wl.BufferReleaseEvent) { b.busy = false }

func (b *shmBuffer) destroy() {
	b.buf.Destroy()
	_ = wlos.Munmap(b.data)
}

// newSHMBuffer allocates one XRGB8888 shm buffer of width×height via shm.
func newSHMBuffer(shm *wl.Shm, width, height int) (*shmBuffer, error) {
	stride := width * 4
	size := stride * height

	fd, err := wlos.CreateAnonymousFile(int64(size))
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	data, err := wlos.Mmap(int(fd.Fd()), 0, size, wlos.ProtRead|wlos.ProtWrite, wlos.MapShared)
	if err != nil {
		return nil, err
	}
	pool, err := shm.CreatePool(fd.Fd(), int32(size))
	if err != nil {
		wlos.Munmap(data)
		return nil, err
	}
	buf, err := pool.CreateBuffer(0, int32(width), int32(height), int32(stride), wl.ShmFormatXrgb8888)
	pool.Destroy()
	if err != nil {
		wlos.Munmap(data)
		return nil, err
	}
	b := &shmBuffer{buf: buf, data: data}
	buf.AddReleaseHandler(b)
	return b, nil
}

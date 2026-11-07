package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"syscall"
	"testing"
	"time"
)

// hookGatedWrite is the all-or-nothing strategy of platforms whose PIPE_BUF
// is below one maximal item: it writes only once the whole output fits,
// so partial room and a stalled reader leave no truncated prefix.
func TestHookGatedWrite(t *testing.T) {
	data := bytes.Repeat([]byte("j"), 3000)
	type fake struct {
		rooms  []int // successive room() answers; the last repeats
		writes []func([]byte) (int, error)
	}
	full := func(p []byte) (int, error) { return len(p), nil }
	for _, tc := range []struct {
		name    string
		data    []byte
		f       fake
		roomErr error
		want    error
		written int // complete writes expected
		calls   int // write calls expected
	}{
		{name: "stalled with partial room", data: data, f: fake{rooms: []int{len(data) - 1}}, want: context.DeadlineExceeded},
		{name: "reader drains", data: data, f: fake{rooms: []int{10, 512, len(data)}, writes: []func([]byte) (int, error){full}}, written: 1, calls: 1},
		{name: "EAGAIN retries", data: data, f: fake{rooms: []int{1 << 16}, writes: []func([]byte) (int, error){
			func([]byte) (int, error) { return 0, syscall.EAGAIN }, full,
		}}, written: 1, calls: 2},
		{name: "short write reported", data: data, f: fake{rooms: []int{1 << 16}, writes: []func([]byte) (int, error){
			func([]byte) (int, error) { return 5, nil },
		}}, want: io.ErrShortWrite, calls: 1},
		{name: "closed reader", data: data, f: fake{rooms: []int{1 << 16}, writes: []func([]byte) (int, error){
			func([]byte) (int, error) { return 0, syscall.EPIPE },
		}}, want: syscall.EPIPE, calls: 1},
		{name: "room error", data: data, roomErr: syscall.EBADF, want: syscall.EBADF},
		{name: "over the cap", data: make([]byte, hookPipeAtomicBytes+1), f: fake{rooms: []int{1 << 20}}, want: errHookOutputTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			polls, calls, written := 0, 0, 0
			room := func() (int, error) {
				if tc.roomErr != nil {
					return 0, tc.roomErr
				}
				r := tc.f.rooms[min(polls, len(tc.f.rooms)-1)]
				polls++
				return r, nil
			}
			write := func(p []byte) (int, error) {
				if len(p) > tc.f.rooms[min(polls-1, len(tc.f.rooms)-1)] {
					t.Fatalf("wrote %d bytes into %d free", len(p), tc.f.rooms[polls-1])
				}
				if calls >= len(tc.f.writes) {
					t.Fatal("unexpected write")
				}
				n, err := tc.f.writes[calls](p)
				calls++
				if err == nil && n == len(p) {
					written++
				}
				return n, err
			}
			err := hookGatedWrite(ctx, tc.data, room, write)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
			if calls != tc.calls || written != tc.written {
				t.Fatalf("write calls=%d complete=%d, want %d/%d", calls, written, tc.calls, tc.written)
			}
		})
	}
}

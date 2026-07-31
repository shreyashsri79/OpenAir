package files

import (
	"errors"
	"testing"
)

// TestChunkPlanCoversExactlyOnce is oabench/bench/bench_test.go's coverage test,
// ported. It is the test that matters most in this package: a plan with a gap
// silently truncates a file and a plan with an overlap silently corrupts one,
// and neither shows up as an error anywhere else.
func TestChunkPlanCoversExactlyOnce(t *testing.T) {
	const total, chunk = 10*1024 + 7, 1024 // deliberately not a clean multiple

	plan, err := NewPlan([]uint64{total}, chunk)
	if err != nil {
		t.Fatal(err)
	}
	seen := make([]bool, total)
	var sum uint64
	for {
		c, ok := plan.Claim()
		if !ok {
			break
		}
		if c.Size == 0 || uint64(c.Size) > chunk {
			t.Fatalf("chunk at %d has size %d, want 1..%d", c.Offset, c.Size, chunk)
		}
		for i := c.Offset; i < c.Offset+uint64(c.Size); i++ {
			if seen[i] {
				t.Fatalf("byte %d claimed twice", i)
			}
			seen[i] = true
		}
		sum += uint64(c.Size)
	}
	if sum != total {
		t.Errorf("plan covered %d bytes, want %d", sum, total)
	}
	for i, ok := range seen {
		if !ok {
			t.Fatalf("byte %d never claimed", i)
		}
	}
}

// TestChunkPlanMultiFileCoversExactlyOnce is the same guarantee across the
// concatenated byte space of several files, including empty ones, a file that
// is an exact multiple of the chunk size, and a one-byte tail.
func TestChunkPlanMultiFileCoversExactlyOnce(t *testing.T) {
	const chunk = 1024
	sizes := []uint64{0, 1, chunk, chunk + 1, 3*chunk - 1, 0, 5 * chunk, 7}

	plan, err := NewPlan(sizes, chunk)
	if err != nil {
		t.Fatal(err)
	}
	var total uint64
	for _, s := range sizes {
		total += s
	}
	if plan.TotalBytes() != total {
		t.Fatalf("TotalBytes = %d, want %d", plan.TotalBytes(), total)
	}

	seen := make([]bool, total)
	// Per-file coverage as well: the global offset is what goes on the wire,
	// but FileIndex/FileOffset are what the receiver writes with, and a plan
	// where the two disagree writes the right bytes into the wrong file.
	perFile := make([][]bool, len(sizes))
	for i, s := range sizes {
		perFile[i] = make([]bool, s)
	}

	var sum uint64
	var starts []uint64
	for _, s := range sizes {
		starts = append(starts, sum)
		sum += s
	}

	sum = 0
	for {
		c, ok := plan.Claim()
		if !ok {
			break
		}
		if c.Size == 0 || uint64(c.Size) > chunk {
			t.Fatalf("chunk %d has size %d", c.Index, c.Size)
		}
		if got := starts[c.FileIndex] + c.FileOffset; got != c.Offset {
			t.Fatalf("chunk %d: global offset %d but file %d offset %d starts at %d",
				c.Index, c.Offset, c.FileIndex, c.FileOffset, starts[c.FileIndex])
		}
		for i := uint64(0); i < uint64(c.Size); i++ {
			if seen[c.Offset+i] {
				t.Fatalf("global byte %d claimed twice", c.Offset+i)
			}
			seen[c.Offset+i] = true
			if perFile[c.FileIndex][c.FileOffset+i] {
				t.Fatalf("file %d byte %d claimed twice", c.FileIndex, c.FileOffset+i)
			}
			perFile[c.FileIndex][c.FileOffset+i] = true
		}
		sum += uint64(c.Size)
	}
	if sum != total {
		t.Errorf("plan covered %d bytes, want %d", sum, total)
	}
	for i, ok := range seen {
		if !ok {
			t.Fatalf("global byte %d never claimed", i)
		}
	}
}

// A chunk must never span a file boundary: the 12-byte frame carries no file
// identifier, so a chunk that straddled two files could not be written.
func TestChunkNeverSpansFiles(t *testing.T) {
	const chunk = 4096
	plan, err := NewPlan([]uint64{chunk + 10, chunk - 10, 1}, chunk)
	if err != nil {
		t.Fatal(err)
	}
	sizes := []uint64{chunk + 10, chunk - 10, 1}
	for i := uint64(0); i < plan.Count(); i++ {
		c, ok := plan.Chunk(i)
		if !ok {
			t.Fatalf("chunk %d missing", i)
		}
		if c.FileOffset+uint64(c.Size) > sizes[c.FileIndex] {
			t.Fatalf("chunk %d runs past the end of file %d", i, c.FileIndex)
		}
	}
}

func TestPlanRejectsBadChunkSize(t *testing.T) {
	for _, cs := range []uint64{0, MaxChunkSize + 1} {
		if _, err := NewPlan([]uint64{1}, cs); !errors.Is(err, ErrBadPlan) {
			t.Errorf("NewPlan(chunkSize=%d) error = %v, want ErrBadPlan", cs, err)
		}
	}
}

// Locate is the receiver's guard: a peer must not be able to write arbitrary
// bytes at an arbitrary offset just by lying in a chunk header.
func TestPlanLocateRejectsMalformedFrames(t *testing.T) {
	const chunk = 1024
	plan, err := NewPlan([]uint64{3*chunk + 5, chunk}, chunk)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := plan.Locate(0, chunk); err != nil {
		t.Fatalf("valid frame rejected: %v", err)
	}
	tail, _ := plan.Chunk(3)
	if _, err := plan.Locate(tail.Offset, tail.Size); err != nil {
		t.Fatalf("valid tail frame rejected: %v", err)
	}

	cases := []struct {
		name   string
		offset uint64
		size   uint32
	}{
		{"unaligned offset", 1, chunk},
		{"offset past total", plan.TotalBytes(), 1},
		{"zero size", 0, 0},
		{"size beyond chunk size", 0, chunk + 1},
		{"full size on a short tail", tail.Offset, chunk},
		{"short size on a full chunk", 0, chunk - 1},
	}
	for _, c := range cases {
		if _, err := plan.Locate(c.offset, c.size); err == nil {
			t.Errorf("%s: Locate(%d, %d) accepted, want rejection", c.name, c.offset, c.size)
		}
	}
}

// SetHave is the resume path: chunks the receiver already verified are never
// claimed again.
func TestPlanSetHaveSkipsClaimedChunks(t *testing.T) {
	const chunk = 512
	plan, err := NewPlan([]uint64{10 * chunk}, chunk)
	if err != nil {
		t.Fatal(err)
	}
	have := []uint64{0, 3, 4, 9}
	plan.SetHave(have)

	got := map[uint64]bool{}
	for {
		c, ok := plan.Claim()
		if !ok {
			break
		}
		got[c.Index] = true
	}
	for _, i := range have {
		if got[i] {
			t.Errorf("chunk %d was claimed despite being in have_chunks", i)
		}
	}
	if len(got) != int(plan.Count())-len(have) {
		t.Errorf("claimed %d chunks, want %d", len(got), int(plan.Count())-len(have))
	}
}

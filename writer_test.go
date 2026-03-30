package gobzip2

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os/exec"
	"runtime"
	"testing"
)

func TestParallelRoundTrip(t *testing.T) {
	// Generate test data large enough to produce multiple blocks at level 1
	rng := rand.New(rand.NewSource(42))
	data := make([]byte, 500000) // 500KB → 5 blocks at level 1
	for i := range data {
		data[i] = byte(rng.Intn(256))
	}

	for _, conc := range []int{1, 2, 4, runtime.NumCPU()} {
		t.Run(fmt.Sprintf("concurrency=%d", conc), func(t *testing.T) {
			var buf bytes.Buffer
			w, err := NewWriterOptions(&buf, &WriterOptions{Level: 1, Concurrency: conc})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}

			r := NewReader(&buf)
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("decompress error: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Errorf("data mismatch: got %d bytes, want %d bytes", len(got), len(data))
			}
		})
	}
}

func TestParallelCrossCompat(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	data := make([]byte, 300000)
	for i := range data {
		data[i] = byte(rng.Intn(256))
	}

	var buf bytes.Buffer
	w, _ := NewWriterOptions(&buf, &WriterOptions{Level: 1, Concurrency: 4})
	w.Write(data)
	w.Close()

	// Verify with system bzip2
	cmd := exec.Command("bzip2", "-d")
	cmd.Stdin = bytes.NewReader(buf.Bytes())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("system bzip2 -d failed: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Errorf("system bzip2 decompressed incorrectly: got %d bytes, want %d", len(out), len(data))
	}
}

// ---- Benchmarks ----

// benchData generates deterministic test data that compresses reasonably.
func benchData(size int) []byte {
	rng := rand.New(rand.NewSource(12345))
	data := make([]byte, size)
	// Mix of patterns: some repetitive, some random
	for i := range data {
		if i%100 < 70 {
			data[i] = byte(rng.Intn(20)) // biased towards small values
		} else {
			data[i] = byte(rng.Intn(256))
		}
	}
	return data
}

func benchmarkCompress(b *testing.B, level, concurrency, dataSize int) {
	data := benchData(dataSize)
	b.SetBytes(int64(dataSize))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		w, _ := NewWriterOptions(io.Discard, &WriterOptions{Level: level, Concurrency: concurrency})
		w.Write(data)
		w.Close()
	}
}

func BenchmarkCompress1MB_Single(b *testing.B) {
	benchmarkCompress(b, 1, 1, 1<<20)
}

func BenchmarkCompress1MB_Parallel2(b *testing.B) {
	benchmarkCompress(b, 1, 2, 1<<20)
}

func BenchmarkCompress1MB_Parallel4(b *testing.B) {
	benchmarkCompress(b, 1, 4, 1<<20)
}

func BenchmarkCompress1MB_ParallelCPU(b *testing.B) {
	benchmarkCompress(b, 1, runtime.NumCPU(), 1<<20)
}

func BenchmarkCompress10MB_Single(b *testing.B) {
	benchmarkCompress(b, 1, 1, 10<<20)
}

func BenchmarkCompress10MB_Parallel2(b *testing.B) {
	benchmarkCompress(b, 1, 2, 10<<20)
}

func BenchmarkCompress10MB_Parallel4(b *testing.B) {
	benchmarkCompress(b, 1, 4, 10<<20)
}

func BenchmarkCompress10MB_ParallelCPU(b *testing.B) {
	benchmarkCompress(b, 1, runtime.NumCPU(), 10<<20)
}

func BenchmarkDecompress(b *testing.B) {
	data := benchData(1 << 20)
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Write(data)
	w.Close()
	compressed := buf.Bytes()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		r := NewReader(bytes.NewReader(compressed))
		io.Copy(io.Discard, r)
	}
}

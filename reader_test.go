package gobzip2

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"testing"
)

func TestDecompressSamples(t *testing.T) {
	samples := []struct {
		bz2 string
		ref string
	}{
		{"testdata/sample1.bz2", "testdata/sample1.ref"},
		{"testdata/sample2.bz2", "testdata/sample2.ref"},
		{"testdata/sample3.bz2", "testdata/sample3.ref"},
	}

	for _, s := range samples {
		t.Run(s.bz2, func(t *testing.T) {
			bz2Data, err := os.Open(s.bz2)
			if err != nil {
				t.Fatal(err)
			}
			defer bz2Data.Close()

			refData, err := os.ReadFile(s.ref)
			if err != nil {
				t.Fatal(err)
			}

			reader := NewReader(bz2Data)
			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(got, refData) {
				t.Errorf("decompressed data mismatch: got %d bytes, want %d bytes", len(got), len(refData))
				// Show first difference
				for i := 0; i < len(got) && i < len(refData); i++ {
					if got[i] != refData[i] {
						t.Errorf("first difference at byte %d: got 0x%02x, want 0x%02x", i, got[i], refData[i])
						break
					}
				}
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"hello", []byte("hello")},
		{"zeros", make([]byte, 10000)},
		{"sequence", func() []byte {
			b := make([]byte, 1000)
			for i := range b {
				b[i] = byte(i % 256)
			}
			return b
		}()},
		{"repeated", bytes.Repeat([]byte("abcdefghij"), 10000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewWriter(&buf)
			if len(tt.data) > 0 {
				_, err := w.Write(tt.data)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}

			r := NewReader(&buf)
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("decompress error: %v", err)
			}
			if !bytes.Equal(got, tt.data) {
				t.Errorf("data mismatch: got %d bytes, want %d bytes", len(got), len(tt.data))
			}
		})
	}
}

func TestCrossCompatibility(t *testing.T) {
	// Test that our compressor output can be decompressed by system bzip2,
	// and that system bzip2 output can be decompressed by our reader.
	testData := []byte("The quick brown fox jumps over the lazy dog. Pack my box with five dozen liquor jugs.")

	// Compress with our writer
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Write(testData)
	w.Close()

	// Decompress with system bzip2
	cmd := exec.Command("bzip2", "-d")
	cmd.Stdin = bytes.NewReader(buf.Bytes())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("system bzip2 -d failed: %v", err)
	}
	if !bytes.Equal(out, testData) {
		t.Errorf("system bzip2 decompressed incorrectly: got %q", string(out))
	}

	// Compress with system bzip2
	cmd = exec.Command("bzip2")
	cmd.Stdin = bytes.NewReader(testData)
	sysBz2, err := cmd.Output()
	if err != nil {
		t.Fatalf("system bzip2 compress failed: %v", err)
	}

	// Decompress with our reader
	r := NewReader(bytes.NewReader(sysBz2))
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("our reader failed on system bzip2 output: %v", err)
	}
	if !bytes.Equal(got, testData) {
		t.Errorf("our reader decompressed incorrectly: got %q", string(got))
	}
}

func TestDecompressSmall(t *testing.T) {
	// "hello" compressed with bzip2
	// Generated with: echo -n "hello" | bzip2 | xxd -i
	compressed := []byte{
		0x42, 0x5a, 0x68, 0x39, 0x31, 0x41, 0x59, 0x26,
		0x53, 0x59, 0x19, 0x31, 0x65, 0x3d, 0x00, 0x00,
		0x00, 0x81, 0x00, 0x02, 0x44, 0xa0, 0x00, 0x21,
		0x9a, 0x68, 0x33, 0x4d, 0x07, 0x33, 0x8b, 0xb9,
		0x22, 0x9c, 0x28, 0x48, 0x0c, 0x98, 0xb2, 0x9e,
		0x80,
	}

	reader := NewReader(bytes.NewReader(compressed))
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("decompression error: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", string(got), "hello")
	}
}

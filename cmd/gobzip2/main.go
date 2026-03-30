// Command gobzip2 compresses and decompresses files in bzip2 format.
//
// Usage:
//
//	gobzip2 [-d] [-z] [-k] [-c] [-f] [-1...-9] [file ...]
//
// When invoked as "bunzip2" or "gobunzip2", defaults to decompression mode.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/KarpelesLab/gobzip2"
)

var (
	decompress bool
	compress   bool
	keep       bool
	stdout     bool
	force      bool
	level      int
	test       bool
	verbose    bool
)

func init() {
	flag.BoolVar(&decompress, "d", false, "decompress")
	flag.BoolVar(&compress, "z", false, "compress (default)")
	flag.BoolVar(&keep, "k", false, "keep input files")
	flag.BoolVar(&stdout, "c", false, "write to stdout")
	flag.BoolVar(&force, "f", false, "force overwrite")
	flag.BoolVar(&test, "t", false, "test integrity")
	flag.BoolVar(&verbose, "v", false, "verbose")

	// Compression levels
	for i := 1; i <= 9; i++ {
		func(lv int) {
			flag.BoolVar(new(bool), fmt.Sprintf("%d", lv), false, fmt.Sprintf("block size %d00k", lv))
		}(i)
	}
}

func main() {
	flag.Parse()

	// Parse level from flags
	level = 9
	for i := 1; i <= 9; i++ {
		f := flag.Lookup(fmt.Sprintf("%d", i))
		if f != nil {
			if g, ok := f.Value.(flag.Getter); ok {
				if g.Get().(bool) {
					level = i
				}
			}
		}
	}

	// Detect mode from argv[0]
	base := filepath.Base(os.Args[0])
	if strings.Contains(base, "bunzip2") || strings.Contains(base, "bzcat") {
		decompress = true
	}
	if strings.Contains(base, "bzcat") {
		stdout = true
	}

	if !decompress && !compress && !test {
		compress = true
	}
	if test {
		decompress = true
	}

	args := flag.Args()
	if len(args) == 0 {
		// Read from stdin, write to stdout
		stdout = true
		if decompress {
			if err := decompressStream(os.Stdin, os.Stdout); err != nil {
				fatal(err)
			}
		} else {
			if err := compressStream(os.Stdin, os.Stdout); err != nil {
				fatal(err)
			}
		}
		return
	}

	for _, name := range args {
		if decompress {
			if err := decompressFile(name); err != nil {
				fmt.Fprintf(os.Stderr, "gobzip2: %s: %v\n", name, err)
				os.Exit(1)
			}
		} else {
			if err := compressFile(name); err != nil {
				fmt.Fprintf(os.Stderr, "gobzip2: %s: %v\n", name, err)
				os.Exit(1)
			}
		}
	}
}

func compressStream(r io.Reader, w io.Writer) error {
	bw, err := gobzip2.NewWriterLevel(w, level)
	if err != nil {
		return err
	}
	if _, err := io.Copy(bw, r); err != nil {
		return err
	}
	return bw.Close()
}

func decompressStream(r io.Reader, w io.Writer) error {
	br := gobzip2.NewReader(r)
	_, err := io.Copy(w, br)
	return err
}

func compressFile(name string) error {
	outName := name + ".bz2"

	in, err := os.Open(name)
	if err != nil {
		return err
	}
	defer in.Close()

	if stdout {
		return compressStream(in, os.Stdout)
	}

	if !force {
		if _, err := os.Stat(outName); err == nil {
			return fmt.Errorf("output file %s already exists", outName)
		}
	}

	out, err := os.Create(outName)
	if err != nil {
		return err
	}

	if err := compressStream(in, out); err != nil {
		out.Close()
		os.Remove(outName)
		return err
	}

	if err := out.Close(); err != nil {
		return err
	}

	// Preserve file permissions
	if info, err := os.Stat(name); err == nil {
		os.Chmod(outName, info.Mode())
	}

	if verbose {
		inInfo, _ := os.Stat(name)
		outInfo, _ := os.Stat(outName)
		if inInfo != nil && outInfo != nil {
			ratio := float64(outInfo.Size()) / float64(inInfo.Size()) * 100
			fmt.Fprintf(os.Stderr, "  %s: %.2f%% -- replaced with %s\n", name, ratio, outName)
		}
	}

	if !keep {
		os.Remove(name)
	}
	return nil
}

func decompressFile(name string) error {
	outName := strings.TrimSuffix(name, ".bz2")
	if outName == name {
		outName = name + ".out"
	}

	in, err := os.Open(name)
	if err != nil {
		return err
	}
	defer in.Close()

	if test {
		return decompressStream(in, io.Discard)
	}

	if stdout {
		return decompressStream(in, os.Stdout)
	}

	if !force {
		if _, err := os.Stat(outName); err == nil {
			return fmt.Errorf("output file %s already exists", outName)
		}
	}

	out, err := os.Create(outName)
	if err != nil {
		return err
	}

	if err := decompressStream(in, out); err != nil {
		out.Close()
		os.Remove(outName)
		return err
	}

	if err := out.Close(); err != nil {
		return err
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "  %s: done\n", name)
	}

	if !keep {
		os.Remove(name)
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "gobzip2: %v\n", err)
	os.Exit(1)
}

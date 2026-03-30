[![GoDoc](https://godoc.org/github.com/KarpelesLab/gobzip2?status.svg)](https://godoc.org/github.com/KarpelesLab/gobzip2)
[![Go Report Card](https://goreportcard.com/badge/github.com/KarpelesLab/gobzip2)](https://goreportcard.com/report/github.com/KarpelesLab/gobzip2)
[![Coverage Status](https://coveralls.io/repos/github/KarpelesLab/gobzip2/badge.svg?branch=master)](https://coveralls.io/github/KarpelesLab/gobzip2?branch=master)

# gobzip2

Pure Go implementation of bzip2 compression and decompression, based on the bzip2 1.0.8 reference C implementation.

Unlike the standard library's `compress/bzip2` which only supports decompression, this package provides both compression and decompression.

## Install

```bash
go get github.com/KarpelesLab/gobzip2
```

## Usage

### Compression

```go
var buf bytes.Buffer
w := gobzip2.NewWriter(&buf)
w.Write([]byte("hello, world"))
w.Close()
```

### Decompression

```go
r := gobzip2.NewReader(bytes.NewReader(compressed))
data, err := io.ReadAll(r)
```

### CLI

```bash
go install github.com/KarpelesLab/gobzip2/cmd/gobzip2@latest

# Compress
gobzip2 file.txt

# Decompress
gobzip2 -d file.txt.bz2

# Pipe
cat file | gobzip2 | gobzip2 -d
```

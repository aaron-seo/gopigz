package main

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"flag"
	"hash"
	"hash/crc32"
	"io"
	"log"
	"os"
	"runtime"
	"strconv"
	"sync"
)

// Declaration of global constants
const (
	BLOCK_SIZE   = 128 * 1024 // 128 KiB
	DICT_SIZE    = 32 * 1024  // 32 KiB
	TRAILER_SIZE = 8
	SUM_SIZE     = 8 // 8 bytes
)

// Parsing processes flag
var processes int

func init() {
	var (
		defaultProcesses = runtime.NumCPU()
		usage            = "Specify number of goroutines to use for compression"
	)
	flag.IntVar(&processes, "processes", defaultProcesses, usage)
	flag.IntVar(&processes, "p", defaultProcesses, usage)
}

// checksum globals
var checksum hash.Hash32
var checksumChan chan []byte
var nTotalBytes uint32

// This implementation of concurrent compression utilizes the pipelined,
// fan-out, fan-in concurrency pattern as described in
// https://go.dev/blog/pipelines
// There are three stages for a Block to be pipelined through:
// (1) Read stage
// (2) Compress stage
// (3) Write stage
func main() {
	flag.Parse()

	// Checksum (CRC32-IEEE polynomial)
	checksumChan = make(chan []byte)
	go func() {
		checksum = crc32.NewIEEE()
		for data := range checksumChan {
			checksum.Write(data)
			log.Println("wrote checksum")
		}
		close(checksumChan)
	}()

	r := read()

	c := compress(r)

	//writeHeader()
	for output := range c {
		write(output)
	}
	writeTrailer()

	/*
		compressOutbounds := make([]<-chan *block, processes)
		for p := 0; p < processes; p++ {
			compressOutbounds[p] = compress(r)
		}

		for c := range mergeSlice(compressOutbounds) {
			write(c)
		}
	*/
}

// Read stage
func read() <-chan *block {
	out := make(chan *block)

	go func() {
		// Start reading input from Stdin in byte array buffers with BLOCK_SIZE
		inputBuffer := make([]byte, BLOCK_SIZE, BLOCK_SIZE)

		var numBlocks int

		reader := bufio.NewReader(os.Stdin)
		numBytes, err := reader.Read(inputBuffer)
		for err != io.EOF {
			numBytes, err = reader.Read(inputBuffer)

			// check if readBuffer is the last block in the buffer
			isLastBlock := false
			_, err = reader.Peek(1)
			if err == bufio.ErrNegativeCount {
				isLastBlock = true
			}

			numBlocks++

			b := block{
				Index:     numBlocks,
				LastBlock: isLastBlock,
				RawData:   inputBuffer,
				nRawBytes: numBytes,
			}

			// checksum
			checksumChan <- inputBuffer
			nTotalBytes += uint32(numBytes)

			log.Println("read block#" + strconv.Itoa(b.Index))
			out <- &b
		}
		close(out)
	}()

	return out
}

// Compress stage
func compress(in <-chan *block) <-chan *block {
	out := make(chan *block)

	go func() {

		for b := range in {
			var buffer bytes.Buffer

			flateWriter, err := flate.NewWriter(&buffer, flate.DefaultCompression)
			if err != nil {
				log.Fatal(err)
			}

			n, err := io.Copy(flateWriter, bytes.NewReader(b.RawData))
			if err != nil {
				log.Fatal(err)
			}

			if !b.LastBlock {
				if err := flateWriter.Flush(); err != nil {
					log.Fatal(err)
				}
			}

			if err := flateWriter.Close(); err != nil {
				log.Fatal(err)
			}

			b.CompressedData = buffer.Bytes()
			b.nCompressedBytes = int(n)

			out <- b
			log.Println("compressed block#" + strconv.Itoa(b.Index))
		}
		close(out)
	}()

	return out
}

func writeHeader() {
	w := bufio.NewWriter(os.Stdout)

	headerBytes := make([]byte, 10)
	headerBytes[0] = 0x1f
	headerBytes[1] = 0x8b
	headerBytes[2] = 0x08
	headerBytes[3] = 0x00
	headerBytes[4] = 0x00
	headerBytes[5] = 0x00
	headerBytes[6] = 0x00
	headerBytes[7] = 0x00
	headerBytes[8] = 0x00
	headerBytes[9] = 0x03

	w.Write(headerBytes)
	log.Println("wrote header")
}

func writeTrailer() {
	w := bufio.NewWriter(os.Stdout)

	trailerBuf := make([]byte, TRAILER_SIZE)
	le := binary.LittleEndian
	le.PutUint32(trailerBuf[:4], checksum.Sum32())
	le.PutUint32(trailerBuf[4:8], nTotalBytes)
	w.Write(trailerBuf)
	log.Println("wrote trailer")

	w.Flush()
}

// Write stage
func write(b *block) {
	w := bufio.NewWriter(os.Stdout)
	w.Write(b.CompressedData)

	log.Println("wrote block#" + strconv.Itoa(b.Index))
}

// mergeList fans-in slice of results from the compress goroutines into the write stage
func mergeSlice(compressOutbounds []<-chan *block) <-chan *block {
	log.Println("merging")

	var wg sync.WaitGroup
	out := make(chan *block)

	wg.Add(len(compressOutbounds))
	for i := 0; i < len(compressOutbounds); i++ {
		go func() {
			for b := range compressOutbounds[i] {
				out <- b
			}
			wg.Done()
		}()
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	return out
}

func merge(cs ...<-chan *block) <-chan *block {
	return nil
}

type block struct {
	Index            int
	LastBlock        bool
	RawData          []byte
	CompressedData   []byte
	nRawBytes        int
	nCompressedBytes int
	Err              error
}

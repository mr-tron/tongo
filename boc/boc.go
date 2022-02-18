package boc

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math"
)

var reachBocMagicPrefix = []byte{
	0xb5, 0xee, 0x9c, 0x72,
}

var leanBocMagicPrefix = []byte{
	0x68, 0xff, 0x65, 0xf3,
}

var leanBocMagicPrefixCRC = []byte{
	0xac, 0xc3, 0xa7, 0x28,
}

var crcTable = crc32.MakeTable(crc32.Castagnoli)

func ByteArrayEquals(a []byte, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}

func readNBytesUIntFromArray(n int, arr []byte) uint {
	var res uint = 0
	for i := 0; i < n; i++ {
		res *= 256
		res += uint(arr[i])
	}
	return res
}

type bocHeader struct {
	hasIdx       bool
	hashCrc32    bool
	hasCacheBits bool
	flags        int
	sizeBytes    int
	cellsNum     uint
	rootsNum     uint
	absentNum    uint
	totCellsSize uint
	rootList     []uint
	index        []uint
	cellsData    []byte
}

func parseBocHeader(boc []byte) (*bocHeader, error) {

	var originalBoc = make([]byte, len(boc))
	copy(originalBoc, boc)

	if len(boc) < 4+1 {
		return nil, errors.New("not enough bytes for magic prefix")
	}

	var prefix = boc[0:4]
	boc = boc[4:]

	var hasIdx = false
	var hashCrc32 = false
	var hasCacheBits = false
	var flags = 0
	var sizeBytes = 0

	if ByteArrayEquals(prefix, reachBocMagicPrefix) {
		var flagsByte = boc[0]
		hasIdx = (flagsByte & 128) > 0
		hashCrc32 = (flagsByte & 64) > 0
		hasCacheBits = (flagsByte & 32) > 0
		flags = int((flagsByte&16)*2 + (flagsByte & 8))
		sizeBytes = int(flagsByte % 8)
	} else if ByteArrayEquals(prefix, leanBocMagicPrefix) {
		hasIdx = true
		hashCrc32 = false
		hasCacheBits = false
		flags = 0
		sizeBytes = int(boc[0])
	} else if ByteArrayEquals(prefix, leanBocMagicPrefixCRC) {
		hasIdx = true
		hashCrc32 = true
		hasCacheBits = false
		flags = 0
		sizeBytes = int(boc[0])
	} else {
		return nil, errors.New("unknown magic prefix")
	}

	boc = boc[1:]
	if len(boc) < 1+5*sizeBytes {
		return nil, errors.New("not enough bytes for encoding cells counters")
	}

	offsetBytes := int(boc[0])
	boc = boc[1:]
	cellsNum := readNBytesUIntFromArray(sizeBytes, boc)
	boc = boc[sizeBytes:]
	rootsNum := readNBytesUIntFromArray(sizeBytes, boc)
	boc = boc[sizeBytes:]
	absentNum := readNBytesUIntFromArray(sizeBytes, boc)
	boc = boc[sizeBytes:]
	totCellsSize := readNBytesUIntFromArray(offsetBytes, boc)
	boc = boc[offsetBytes:]

	if len(boc) < int(rootsNum)*sizeBytes {
		return nil, errors.New("not enough bytes for encoding root cells hashes")
	}

	// Roots
	rootList := make([]uint, 0)
	for i := 0; i < int(rootsNum); i++ {
		rootList = append(rootList, readNBytesUIntFromArray(sizeBytes, boc))
		boc = boc[sizeBytes:]
	}

	// Index
	index := make([]uint, 0)
	if hasIdx {
		if len(boc) < offsetBytes*int(cellsNum) {
			return nil, errors.New("not enough bytes for index encoding")
		}
		for i := 0; i < int(cellsNum); i++ {
			index = append(index, readNBytesUIntFromArray(offsetBytes, boc))
			boc = boc[offsetBytes:]
		}
	}

	// Cells
	if len(boc) < int(totCellsSize) {
		return nil, errors.New("not enough bytes for cells data")
	}

	cellsData := boc[0:totCellsSize]
	boc = boc[totCellsSize:]

	if hashCrc32 {
		if len(boc) < 4 {
			return nil, errors.New("not enough bytes for crc32c hashsum")
		}
		if binary.LittleEndian.Uint32(boc[0:4]) != crc32.Checksum(originalBoc[0:len(originalBoc)-4], crcTable) {
			return nil, errors.New("crc32c hashsum mismatch")
		}
		boc = boc[4:]
	}

	if len(boc) > 0 {
		return nil, errors.New("too much bytes in provided boc")
	}

	return &bocHeader{
		hasIdx,
		hashCrc32,
		hasCacheBits,
		flags,
		sizeBytes,
		cellsNum,
		rootsNum,
		absentNum,
		totCellsSize,
		rootList,
		index,
		cellsData,
	}, nil
}

func deserializeCellData(cellData []byte, referenceIndexSize int) (*Cell, []int, []byte, error) {
	if len(cellData) < 2 {
		return nil, nil, nil, errors.New("not enough bytes to encode cell descriptors")
	}

	d1 := cellData[0]
	d2 := cellData[1]
	cellData = cellData[2:]

	isExotic := (d1 & 8) > 0
	refNum := int(d1 % 8)
	dataBytesSize := int(math.Ceil(float64(d2) / float64(2)))
	fullfilledBytes := ^(d2 % 2) > 0

	var cell = NewCell(isExotic)
	var refs = make([]int, 0)

	if len(cellData) < dataBytesSize+referenceIndexSize*refNum {
		return nil, nil, nil, errors.New("not enough bytes to encode cell data")
	}

	cell.Bits.SetTopUppedArray(cellData[0:dataBytesSize], fullfilledBytes)
	cellData = cellData[dataBytesSize:]

	for i := 0; i < refNum; i++ {
		refs = append(refs, int(readNBytesUIntFromArray(referenceIndexSize, cellData)))
		cellData = cellData[referenceIndexSize:]
	}

	return &cell, refs, cellData, nil
}

func DeserializeBoc(boc []byte) ([]*Cell, error) {
	header, _ := parseBocHeader(boc)

	cellsData := header.cellsData
	cellsArray := make([]*Cell, 0)
	refsArray := make([][]int, 0)

	for i := 0; i < int(header.cellsNum); i++ {
		cell, refs, residue, _ := deserializeCellData(cellsData, header.sizeBytes)
		cellsData = residue
		cellsArray = append(cellsArray, cell)
		refsArray = append(refsArray, refs)
	}

	for i := int(header.cellsNum - 1); i >= 0; i-- {
		c := refsArray[i]

		for ri := 0; ri < len(c); ri++ {
			r := c[ri]
			if r < int(i) {
				return nil, errors.New("topological order is broken")
			}
			cellsArray[i].refs[ri] = cellsArray[r]
		}
	}

	rootCells := make([]*Cell, 0)

	for _, item := range header.rootList {
		rootCells = append(rootCells, cellsArray[item])
	}

	return rootCells, nil
}
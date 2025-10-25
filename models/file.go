package models

import (
	"os"
	"time"
)

type FileChunk struct {
	FileID   uint32
	Seq      uint16
	Data     []byte
	ClientID uint16
}

type FileMeta struct {
	FileID    uint32
	FileSize  uint32
	FileName  []byte
	ChunkSize uint16
	ClientID  uint16
}

type ReceiveSession struct {
	FileMeta

	File       *os.File
	Expected   uint16
	Received   uint16
	TotalChunk uint16
	Chunks     map[uint16]bool
	AckChan    chan AckResponse
}

type SendSession struct {
	FileID      uint32
	File        *os.File
	FileSize    uint32
	FileName    string
	TotalChunks uint16
	SentChunks  map[uint16]time.Time // track chunks that have been sent

	CreatedAt time.Time
}

type AckResponse struct {
	Seq    uint16
	Status byte
}

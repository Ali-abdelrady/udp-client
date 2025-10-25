package workers

// import (
// 	"encoding/binary"
// 	"fmt"
// 	"hole-punching-client/models"
// 	"hole-punching-client/udp"
// 	"hole-punching-client/utils"
// 	"os"
// 	"path/filepath"
// 	"sync"
// )

// const (
// 	CHUNKSIZE = 1200
// )

// type FileChunk struct {
// 	Seq      uint32
// 	FileID   uint32
// 	FileSize uint32
// 	Data     []byte
// 	ClientID uint16
// }

// type ReceiveSession struct {
// 	File       *os.File
// 	FileID     uint32
// 	FileName   string
// 	Expected   uint32
// 	Received   uint32
// 	TotalChunk uint32
// 	Chunks     map[uint32]bool
// 	ChunkChan  chan FileChunk
// }

// type SendSession struct {
// 	FileID      uint32
// 	File        *os.File
// 	FileSize    uint32
// 	FileName    string
// 	TotalChunks uint32
// 	MetaSent    bool
// }

// type FileManager struct {
// 	recvFiles map[uint32]*ReceiveSession
// 	sendFiles map[uint32]*SendSession
// 	Ops       chan FileChunk
// 	mu        sync.RWMutex
// 	client    *udp.Client // 👈 reference to UDP client

// }

// type FileMeta struct {
// 	FileID      uint32
// 	FileName    string
// 	FileSize    uint32
// 	TotalChunks uint32
// }

// func NewFileManger(client *udp.Client) *FileManager {
// 	fm := &FileManager{
// 		recvFiles: make(map[uint32]*ReceiveSession),
// 		sendFiles: make(map[uint32]*SendSession),
// 		Ops:       make(chan FileChunk),
// 		client:    client, // 👈 store UDP client

// 	}

// 	go fm.run()

// 	return fm
// }

// func (fm *FileManager) run() {
// 	for chunk := range fm.Ops {
// 		session, exists := fm.recvFiles[chunk.FileID]

// 		// Create new session for new fileId
// 		if !exists && chunk.Seq == 0 {
// 			fmt.Printf("📥 Received Meta From Client%d | FileID=%d | Size=%d bytes\n",
// 				chunk.ClientID, chunk.FileID, chunk.FileSize) // Save file in project root

// 			wd, err := os.Getwd()
// 			if err != nil {
// 				fmt.Println("Error getting working directory:", err)
// 				return
// 			}

// 			filePath := filepath.Join(wd, fmt.Sprintf("client%d_%s", chunk.ClientID, chunk.Data))
// 			file, err := os.Create(filePath)
// 			if err != nil {
// 				fmt.Println("Error creating file:", err)
// 				continue
// 			}

// 			session = &ReceiveSession{
// 				File:       file,
// 				FileID:     chunk.FileID,
// 				FileName:   filePath,
// 				Expected:   chunk.FileSize,
// 				Received:   0,
// 				Chunks:     make(map[uint32]bool),
// 				ChunkChan:  make(chan FileChunk, 50),
// 				TotalChunk: (chunk.FileSize + CHUNKSIZE - 1) / CHUNKSIZE,
// 			}

// 			fm.recvFiles[chunk.FileID] = session

// 			go fm.handleFile(session)

// 			go fm.OnMetaReceived(session)

// 		}

// 		if session == nil {
// 			continue
// 		}

// 		session.ChunkChan <- chunk
// 	}
// }

// func (fm *FileManager) handleFile(session *ReceiveSession) {
// 	for chunk := range session.ChunkChan {
// 		// Check if the chunk duplicated
// 		if session.Chunks[chunk.Seq] {
// 			fmt.Printf("⚠ Duplicate Seq = %d ignored \n", chunk.Seq)
// 			continue
// 		}

// 		session.Chunks[chunk.Seq] = true

// 		offset := int64(chunk.Seq) * int64(CHUNKSIZE)
// 		session.File.WriteAt(chunk.Data, offset)
// 		session.Received += uint32(len(chunk.Data))
// 		fmt.Printf("Recieved From Client%d (%d/%d) seq = %d \n", chunk.ClientID, session.Received, session.Expected, chunk.Seq)

// 		if session.Received >= session.Expected {
// 			fmt.Printf("✅ Client%d File %d done (%.2f KB)\n", chunk.ClientID, chunk.FileID, float64(session.Expected)/1024)
// 			session.File.Close()
// 			close(session.ChunkChan)
// 			delete(fm.recvFiles, chunk.FileID)
// 			return
// 		}
// 	}
// }

// func (fm *FileManager) RegisterFile(path string) (*SendSession, error) {
// 	fm.mu.Lock()
// 	defer fm.mu.Unlock()

// 	file, err := os.Open(path)
// 	if err != nil {
// 		fmt.Println("falied to open file with path: ", "./message")
// 		return nil, err
// 	}

// 	// file Meta
// 	stat, _ := file.Stat()

// 	// Extract meta

// 	// addr := s.clientManger.GetClient(clientId)
// 	fileId := utils.GenerateTimestampID()
// 	fileSize := stat.Size()
// 	totalChunks := uint32((fileSize + CHUNKSIZE - 1) / CHUNKSIZE)
// 	fileName := filepath.Base(path)

// 	session := &SendSession{
// 		FileID:      fileId,
// 		File:        file,
// 		FileSize:    uint32(fileSize),
// 		FileName:    fileName,
// 		TotalChunks: totalChunks,
// 	}

// 	fm.sendFiles[fileId] = session

// 	return session, nil
// }

// func (fm *FileManager) GetChunk(fileID uint32, seq uint32) ([]byte, error) {

// 	fm.mu.RLock()
// 	session, exists := fm.sendFiles[fileID]
// 	fm.mu.RUnlock()

// 	if !exists {
// 		return nil, fmt.Errorf("fileID %d not registered for sending", fileID)
// 	}

// 	var buf []byte
// 	offset := int64(seq) * int64(CHUNKSIZE)
// 	n, err := session.File.ReadAt(buf, offset)
// 	if err != nil {
// 		return nil, err
// 	}

// 	return buf[:n], nil
// }

// func (fm *FileManager) CloseSendFile(fileID uint32) {
// 	fm.mu.Lock()
// 	defer fm.mu.Unlock()

// 	if session, ok := fm.sendFiles[fileID]; ok {
// 		session.File.Close()
// 		delete(fm.sendFiles, fileID)
// 	}
// }

// func (fm *FileManager) OnMetaReceived(session *ReceiveSession) {
// 	for seq := uint32(0); seq < session.TotalChunk; seq++ {
// 		req := make([]byte, 8)
// 		binary.BigEndian.PutUint32(req[0:4], session.FileID)
// 		binary.BigEndian.PutUint32(req[4:8], seq)

// 		doneChan := make(chan bool, 1)
// 		packet := models.Packet{
// 			OpCode:  udp.OpChunkRequest,
// 			Payload: req,
// 			Done:    doneChan,
// 		}

// 		// Send and wait
// 		err := fm.client.SendWithAck(packet)
// 		if err != nil {
// 			fmt.Printf("❌ Chunk %d request failed after retries\n", seq)
// 			continue
// 		}

// 		success := <-doneChan
// 		if success {
// 			fmt.Printf("✅ Requested chunk %d acknowledged\n", seq)
// 		} else {
// 			fmt.Printf("⚠️ Chunk %d failed after timeout\n", seq)
// 		}
// 	}

// }

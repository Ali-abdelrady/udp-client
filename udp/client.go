package udp

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hole-punching-client/models"
	"hole-punching-client/utils"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	BUFFER_SIZE = 65507
	CHUNKSIZE   = 1400
	alpha       = 0.125
	beta        = 0.25
)

const (
	OpRegister            byte = iota // 0
	OpPing                            // 1
	OpMessage                         // 2
	OpPong                            // 3
	OpFileChunk                       //4
	OpAck                             //5
	OpFileMeta                        //6
	OpChunkRequest                    //7
	OpChunkStatusRequest              // 8
	OpChunkStatusResponse             // 9
)

type Client struct {
	ID             int
	parserChan     chan models.RawPacket
	writeChan      chan models.Packet
	generateChan   chan models.Packet
	ackChan        chan models.Packet
	fileMetaChan   chan models.FileMeta
	fileChunksChan chan models.FileChunk

	receivedFiles  sync.Map // map[uint32]*models.ReceiveSession
	sendOutFiles   sync.Map // map[uint32]*models.SendSession
	pendingPackets sync.Map

	//? RTT & Retransmission time out
	smoothedRTT time.Duration
	rttVar      time.Duration
	rto         time.Duration

	//? Congestion Control
	congestionWindow    int // Congestion Window
	slowStartThreshold  int // start slow threshold
	maxCongestionWindow int
	// bytesInFlight       float64
	ackCount int

	// fileManger *workers.FileManager
	// ackManger *workers.AckManager
}

func (c *Client) ConnectToServer(addr string) {

	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		log.Println("failed to resolve udp address, err:", err)
		return
	}

	// Establish the connection
	connection, err := net.DialUDP("udp4", nil, udpAddr)
	if err != nil {
		log.Printf("client%d failed to connect to the server\n", c.ID)
		return
	}
	defer connection.Close()
	log.Println("✅ client connected to the server")

	// Init variables and channels
	c.writeChan = make(chan models.Packet, 50)
	c.parserChan = make(chan models.RawPacket, 50)
	c.generateChan = make(chan models.Packet, 50)
	c.ackChan = make(chan models.Packet, 200)
	c.fileChunksChan = make(chan models.FileChunk, 100)
	c.fileMetaChan = make(chan models.FileMeta, 50)

	c.congestionWindow = 1
	c.slowStartThreshold = 32
	c.maxCongestionWindow = 100 // cap (optional)

	// c.ackManger = workers.NewAckManager()

	// go c.RegisterClient()
	go c.parserWorker()
	go c.writeWorker(connection)
	go c.generatorWorker()
	go c.ackListener()
	go c.retransmissionWorker()
	go c.fileChunkWorker()
	go c.pingServer()
	go c.startInteractiveCommandInput()
	go c.fileMetaWorker()

	go c.cleanupSendOutFiles(10 * time.Minute)

	// 🔹 Defer log for graceful shutdown
	defer utils.PrintApiLog("Server Shutdown")
	c.setupGracfulShutdown()

	// Read response

	buffer := make([]byte, BUFFER_SIZE)
	for {
		n, _, err := connection.ReadFromUDP(buffer)
		if err != nil {
			log.Println("failed to read server response, err:", err)
			return
		}
		if n == 0 {
			continue
		}

		dataCopy := make([]byte, n)
		copy(dataCopy, buffer[:n])

		c.parserChan <- models.RawPacket{Data: dataCopy, Addr: udpAddr}
	}

}

func (c *Client) startInteractiveCommandInput() {
	scanner := bufio.NewScanner(os.Stdin)

	log.Println("🟢 UDP Command Interface Started")
	log.Println("Available commands:")
	log.Println("  message <message>")
	log.Println("  file <filepath>")
	log.Println("  help")
	log.Println("------------------------------")

	for {
		log.Print("> ")

		if !scanner.Scan() {
			log.Println("\n❌ Input closed. Exiting interactive mode.")
			return
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, " ", 3)
		command := strings.ToLower(parts[0])

		switch command {

		// send message <message>
		case "message":
			if len(parts) < 2 {
				log.Println("⚠ Usage: message <text>")
				continue
			}

			// Send Message
			msg := strings.Join(parts[1:], " ")
			c.generateChan <- models.Packet{OpCode: OpMessage, Payload: []byte(msg)}

			// c.sendMessage(string(clientID), msg)

		// send file <clientId> <path>
		case "file":
			if len(parts) < 2 {
				log.Println("⚠ Usage: file <filepath>")
				continue
			}

			filepath := parts[1]
			if _, err := os.Stat(filepath); err != nil {
				log.Println("❌ File not found:", filepath)
				continue
			}

			c.sendFileMeta(filepath)

		// show help
		case "help":
			log.Println("Available commands:")
			log.Println("  message <message>   - send a message to client")
			log.Println("  file <path>         - send a file to client")
			log.Println("  help                - show this help message")

		default:
			log.Printf("❌ Unknown command: '%s' (type 'help' for list)\n", command)
		}
	}
}

// ----- Workers ---------
func (c *Client) writeWorker(conn *net.UDPConn) {
	for {

		pkt := <-c.writeChan
		_, err := conn.Write(pkt.Payload)
		if err != nil {
			log.Println("failed to send packet")
			continue
		}

		// ✅ mark actual send time
		if pkt.Trackable {
			pp := &models.PendingPacket{
				Packet:   pkt,
				SendTime: time.Now(),
				Retries:  0,
				AckChan:  make(chan bool, 1),
			}
			c.pendingPackets.Store(pkt.ID, pp)
		}

		// pacing
		interval := 10 * time.Millisecond
		if c.smoothedRTT > 0 && c.congestionWindow > 0 {
			pacingRate := float64(c.congestionWindow*CHUNKSIZE) / c.smoothedRTT.Seconds()
			interval = time.Duration(float64(CHUNKSIZE) / pacingRate * float64(time.Second))
		}

		time.Sleep(interval)
	}
}

func (c *Client) parserWorker() {

	for {
		raw := <-c.parserChan

		if len(raw.Data) < 9 {
			log.Println("Invalid packet format length")
			continue
		}
		// Packet [opcode 1] [packetId 4] [size 2] [clientId 2] [payload n]
		packet := models.Packet{
			OpCode:   raw.Data[0],
			ID:       binary.BigEndian.Uint32(raw.Data[1:5]),
			Length:   binary.BigEndian.Uint16(raw.Data[5:7]),
			ClientID: binary.BigEndian.Uint16(raw.Data[7:9]),
			Payload:  raw.Data[9:],
			Addr:     raw.Addr,
		}
		switch packet.OpCode {
		case OpAck:
			// log.Println("[Server] -> ", string(packet.Payload))
			c.ackChan <- packet
		case OpPong:
			log.Printf("Client%d Ping Ack Recived \n", c.ID)
			c.ackChan <- packet
		case OpMessage:
			log.Println("[Server] -> ", string(packet.Payload))
			log.Printf("Recived Packet ID : %v\n", packet.ID)

			outgoingPacket := packet
			outgoingPacket.OpCode = OpAck
			c.generateChan <- outgoingPacket
		case OpFileChunk:
			c.onFileChunkReceived(packet)
		case OpFileMeta:
			c.onFileMetaReceived(packet)
		case OpChunkRequest:
			c.onChunkRequestReceived(packet)
		case OpChunkStatusRequest:
			c.onChunkStatusRequestReceived(packet)
		case OpChunkStatusResponse:
			c.onChunkStatusResponseReceived(packet)
		}
	}
}

func (c *Client) generatorWorker() {
	for {
		packet := <-c.generateChan

		var packetID uint32
		isUnreliable := packet.OpCode == OpAck || packet.OpCode == OpChunkStatusResponse

		if isUnreliable {
			// use server packetID
			packetID = packet.ID
		} else {
			// Generate New one
			packetID = utils.GenerateTimestampID()
		}

		finalPayload := c.buildPacketPayload(packet, packetID)
		// log.Printf("Sended Packet ID : %v\n", packetID)

		outgoingPacket := models.Packet{Payload: finalPayload, Addr: packet.Addr, ID: packetID, Done: packet.Done, Trackable: !isUnreliable}
		// log.Printf("Going PacketID = %v , Size = %v \n", outgoingPacket.ID, len(buf))

		c.writeChan <- outgoingPacket
	}
}

func (c *Client) ackListener() {
	for {
		ackPkt := <-c.ackChan

		// Find pending packet
		if value, ok := c.pendingPackets.LoadAndDelete(ackPkt.ID); ok {

			pp := value.(*models.PendingPacket)
			pp.AckChan <- true
			if pp.Packet.Done != nil {
				pp.Packet.Done <- true
			}

			// --- RTT update that doesn't retransmitt ---
			var rtt time.Duration
			if pp.Retries == 0 {
				rtt = time.Since(pp.SendTime)
				c.updateRTT(rtt)
			} else {
				log.Printf("⚠ RTT sample ignored due to retransmission")
			}

			// --- Congestion Control ---
			if c.congestionWindow < c.slowStartThreshold {
				// Slow start: exponential growth
				c.congestionWindow *= 2
				log.Printf("🚀 Slow Start: cwnd doubled to %d\n", c.congestionWindow)
			} else {
				// Congestion avoidance: linear growth (1 packet per RTT)
				c.ackCount++
				if c.ackCount >= c.congestionWindow {
					c.congestionWindow++
					c.ackCount = 0
					log.Printf("📈 Linear Growth: cwnd increased to %d\n", c.congestionWindow)
				}
			}

			// Clamp cwnd to prevent runaway growth
			if c.congestionWindow > c.maxCongestionWindow {
				c.congestionWindow = c.maxCongestionWindow
			}

			// --- Logging ---
			log.Printf(
				"✅ ACK %v | RTT: %v | Smoothed: %v | Var: %v | RTO: %v | cwnd: %d | ssthresh: %d\n",
				pp.Packet.ID,
				rtt,
				c.smoothedRTT,
				c.rttVar,
				c.rto,
				c.congestionWindow,
				c.slowStartThreshold,
			)
		}
	}
}

func (c *Client) retransmissionWorker() {
	maxRetries := 3
	ticker := time.NewTicker(200 * time.Millisecond) // check more frequently

	for range ticker.C {
		now := time.Now()

		c.pendingPackets.Range(func(key, value any) bool {
			pp := value.(*models.PendingPacket)

			// Get latest adaptive RTO
			baseTimeout := c.rto
			if baseTimeout == 0 {
				baseTimeout = 500 * time.Millisecond // default before any RTT samples
			}

			nextRetryDelay := baseTimeout * time.Duration(1<<pp.Retries)

			// Check if timeout expired
			if now.Sub(pp.SendTime) > nextRetryDelay && pp.Retries < maxRetries {
				log.Printf("⏱️ Retrying packet %d (attempt %d, timeout=%v)\n",
					pp.Packet.ID, pp.Retries+1, baseTimeout)

				// Retransmit
				c.writeChan <- pp.Packet
				pp.SendTime = now
				pp.Retries++

			} else if pp.Retries >= maxRetries {
				log.Printf("❌ Packet %d failed after %d retries\n", pp.Packet.ID, pp.Retries)
				c.pendingPackets.Delete(pp.Packet.ID)

				// Congestion reaction
				c.slowStartThreshold = max(2, c.congestionWindow/2)
				c.congestionWindow = 1

				if pp.Packet.Done != nil {
					pp.Packet.Done <- false
				}
			}

			return true
		})
	}
}

func (c *Client) fileMetaWorker() {
	for meta := range c.fileMetaChan {
		c.createSessionFromMeta(meta)
	}
}

func (c *Client) fileChunkWorker() {
	for chunk := range c.fileChunksChan {
		c.appendFileChunk(chunk)
	}
}

func (c *Client) buildPacketPayload(packet models.Packet, packetID uint32) []byte {
	// Packet [opcode 1] [packetId 4] [clientId 2] [payload n]
	buf := make([]byte, 1+4+2+len(packet.Payload))
	buf[0] = packet.OpCode
	binary.BigEndian.PutUint32(buf[1:5], packetID)
	binary.BigEndian.PutUint16(buf[5:7], uint16(c.ID))
	copy(buf[7:], packet.Payload)

	return buf
}

// Senders
func (c *Client) RequestFileChunks(session *models.ReceiveSession) {
	for seq := uint16(0); seq < session.TotalChunk; seq++ {
		if session.Chunks[seq] {
			continue // already received
		}

		fileID := session.FileID

		log.Printf("Requesting chunk %d...\n", seq)

		req := make([]byte, 6)
		binary.BigEndian.PutUint32(req[0:4], fileID)
		binary.BigEndian.PutUint16(req[4:6], seq)
		packet := models.Packet{
			OpCode:   OpChunkRequest,
			Payload:  req,
			ClientID: session.ClientID,
		}

		c.generateChan <- packet

		timeout := time.NewTimer(5 * time.Second)
		var resp models.AckResponse
		select {
		case resp = <-session.AckChan:
			// response received
			log.Println("I Recieved the ack of seq")
		case <-timeout.C:
			log.Printf("⏱ Timeout waiting for chunk %d, sending status query...\n", seq)
			c.sendChunkStatusRequest(packet)
			// Wait again for the status response
			select {
			case resp = <-session.AckChan:
			case <-time.After(3 * time.Second):
				log.Printf("⚠️ No status response for chunk %d, aborting.\n", seq)
				return
			}
		}

		timeout.Stop()

		switch resp.Status {
		case 200:
			log.Printf("Chunk %d received OK\n", seq)
			continue
		case 0: // NotSent
			log.Printf("Chunk %d not sent yet, retrying...\n", seq)
			seq-- // retry
			continue
		case 1: // InProgress
			log.Printf("Chunk %d still being processed, waiting...\n", seq)
			time.Sleep(2 * time.Second)
			seq--
			continue
		case 2: // AlreadySent
			log.Printf("Chunk %d already sent, retrying...\n", seq)
			seq--
			continue
		case 3: // NotFound
			log.Printf("Chunk %d not found, aborting.\n", seq)
			return
		default:
			log.Printf("Unknown status %d for chunk %d\n", resp.Status, seq)
			return
		}
	}
}

func (c *Client) sendChunkStatusRequest(reqPacket models.Packet) {

	fileID := binary.BigEndian.Uint32(reqPacket.Payload[:4])
	seq := binary.BigEndian.Uint16(reqPacket.Payload[4:6])
	clientID := reqPacket.ClientID
	addr := reqPacket.Addr

	log.Printf("📡 Sending status request for FileID=%d, Seq=%d\n", fileID, seq)

	// Build payload: [fileID 4][seq 2]
	buf := make([]byte, 6)
	binary.BigEndian.PutUint32(buf[0:4], fileID)
	binary.BigEndian.PutUint16(buf[4:6], seq)

	statusReqPkt := models.Packet{
		OpCode:   OpChunkStatusRequest,
		Payload:  buf,
		ClientID: clientID,
		Addr:     addr,
	}

	c.generateChan <- statusReqPkt
}

func (c *Client) sendFileMeta(path string) {
	file, err := os.Open(path)
	if err != nil {
		log.Println("falied to open file with path: ", "./message", err)
		return
	}
	// file Meta
	stat, _ := file.Stat()
	fileId := utils.GenerateTimestampID()
	fileSize := stat.Size()
	fileName := filepath.Base(path)
	doneChan := make(chan bool, 1)
	// totalChunks := uint32((fileSize + CHUNKSIZE - 1) / CHUNKSIZE)

	// c.sendOutFiles.Store(fileId, &models.SendSession{File: file, FileID: fileId, FileSize: uint32(fileSize), FileName: fileName, CreatedAt: time.Now()})

	// Build Buffer [fileId 4] [fileSize 4] [ChunkSize 2] [filename n]
	nameBytes := []byte(fileName)
	buf := make([]byte, 4+4+2+len(nameBytes))
	binary.BigEndian.PutUint32(buf[0:4], fileId)
	binary.BigEndian.PutUint32(buf[4:8], uint32(fileSize))
	binary.BigEndian.PutUint16(buf[8:10], uint16(CHUNKSIZE))
	copy(buf[10:], nameBytes)

	pkt := models.Packet{
		OpCode:  OpFileMeta,
		Payload: buf,
		Done:    doneChan,
	}

	c.generateChan <- pkt

	if !<-doneChan {
		log.Println("❌ Didn't get the meta ack")
		return
	}

	// Start Sending file chunks
	time.Sleep(1 * time.Microsecond)

	seq := uint16(0)
	chunk := make([]byte, CHUNKSIZE)

	for {
		n, err := file.Read(chunk)

		if n > 0 {

			//  [fileId 4] [seq 2]
			payload := make([]byte, 6+n)
			binary.BigEndian.PutUint32(payload[:4], fileId)
			binary.BigEndian.PutUint16(payload[4:6], seq)
			copy(payload[6:], chunk[:n])

			c.generateChan <- models.Packet{OpCode: OpFileChunk, Payload: payload}

			seq++
		}

		if err == io.EOF {
			break
		}

		if err != nil {
			log.Println("failed to read files")
			return
		}
	}
}

// f
// Reciever

func (c *Client) pingServer() {
	for {
		c.generateChan <- models.Packet{OpCode: OpPing}
		log.Printf("client%d sent ping\n", c.ID)
		time.Sleep(28 * time.Second) // wait before next
	}
}

func (c *Client) RegisterClient() {

	c.generateChan <- models.Packet{OpCode: OpRegister}
	log.Printf("client%d sent: reqeust for reigstration\n", c.ID)
}

func (c *Client) onChunkRequestReceived(packet models.Packet) {
	// Extract Headers [fileID 4][seq 2]
	fileID := binary.BigEndian.Uint32(packet.Payload[:4])
	seq := binary.BigEndian.Uint16(packet.Payload[4:])

	log.Printf("CHUNK REQ of fileID:%v , seq:%v \n", fileID, seq)

	val, exist := c.sendOutFiles.Load(fileID)
	if !exist {
		log.Printf("fileID %d not registered for sending", fileID)
		return
	}
	session := val.(*models.SendSession)

	// Get Chunk using File Manger
	offset := int64(seq) * int64(CHUNKSIZE)
	chunk := make([]byte, CHUNKSIZE)
	n, err := session.File.ReadAt(chunk, offset)
	if err != nil && err != io.EOF {
		log.Println(err)
		return
	}

	//[fileID 4] [seq 2] [payload n]
	resp := make([]byte, 6+n)
	binary.BigEndian.PutUint32(resp[0:4], fileID)
	binary.BigEndian.PutUint16(resp[4:6], seq)
	copy(resp[6:], chunk[:n])

	respPkt := packet
	respPkt.Payload = resp
	respPkt.OpCode = OpFileChunk

	c.generateChan <- respPkt
}

func (c *Client) onFileMetaReceived(packet models.Packet) {
	// [fileId 4] [fileSize 4] [ChunkSize 2] [filename n]

	fileId := binary.BigEndian.Uint32(packet.Payload[:4])
	fileSize := binary.BigEndian.Uint32(packet.Payload[4:8])
	chunkSize := binary.BigEndian.Uint16(packet.Payload[8:10])
	fileName := string(packet.Payload[10:])
	clientID := packet.ClientID

	log.Printf("📦 Meta from Client%d | FileID=%d | Size=%d bytes | Name=%s | ChunkSize=%d \n",
		clientID, fileId, fileSize, fileName, chunkSize)

	meta := models.FileMeta{
		FileID:    fileId,
		FileSize:  fileSize,
		FileName:  []byte(fileName),
		ChunkSize: chunkSize,
		ClientID:  clientID,
	}

	// Send Meta to fileManger
	c.fileMetaChan <- meta

	// Sends Ack
	ack := packet
	ack.OpCode = OpAck
	c.generateChan <- ack
}

func (c *Client) onFileChunkReceived(packet models.Packet) {
	fileId := binary.BigEndian.Uint32(packet.Payload[:4])
	seq := binary.BigEndian.Uint16(packet.Payload[4:6])
	fileData := append([]byte{}, packet.Payload[6:]...)

	log.Println("I recieved Chunk of Seq =", seq)
	chunk := models.FileChunk{
		FileID:   fileId,
		Seq:      seq,
		Data:     fileData,
		ClientID: packet.ClientID,
	}

	c.fileChunksChan <- chunk

	newPacket := packet
	newPacket.OpCode = OpAck
	newPacket.Payload = []byte{}
	c.generateChan <- newPacket

}

func (c *Client) onChunkStatusResponseReceived(packet models.Packet) {
	fileID := binary.BigEndian.Uint32(packet.Payload[:4])
	seq := binary.BigEndian.Uint16(packet.Payload[4:6])
	status := packet.Payload[6]

	c.ackChan <- packet
	// Send ACk
	// ch := c.ackManger.GetAck(packet.ID)
	// if ch != nil {
	// 	ch <- true
	// }

	val, ok := c.receivedFiles.Load(fileID)
	if !ok {
		log.Printf("⚠️ No active session found for FileID=%d\n", fileID)
		return
	}

	session := val.(*models.ReceiveSession)

	// Push response into the session’s AckChan
	select {
	case session.AckChan <- models.AckResponse{Seq: seq, Status: status}:
		// sent successfully to listener
	default:
		log.Printf("⚠️ AckChan full, dropping status response for FileID=%d Seq=%d\n", fileID, seq)
	}
}

func (c *Client) onChunkStatusRequestReceived(packet models.Packet) {
	fileID := binary.BigEndian.Uint32(packet.Payload[:4])
	seq := binary.BigEndian.Uint16(packet.Payload[4:6])

	log.Printf("Recieved Chunk Ack Request of Seq = %v \n", seq)
	status := byte(3) // Default: Not Found

	if val, ok := c.sendOutFiles.Load(fileID); ok {
		session := val.(*models.SendSession)

		// Ensure map exists
		if session.SentChunks == nil {
			session.SentChunks = make(map[uint16]time.Time)
		}

		if _, exists := session.SentChunks[seq]; exists {
			status = 2 // Already Sent
		} else {
			status = 0 // Not Sent
		}
	}

	resp := make([]byte, 7)
	binary.BigEndian.PutUint32(resp[0:4], fileID)
	binary.BigEndian.PutUint16(resp[4:6], seq)
	resp[6] = status

	response := packet
	response.OpCode = OpChunkStatusResponse
	response.Payload = resp

	c.generateChan <- response
}

// File Manager Handlers
func (c *Client) createSessionFromMeta(meta models.FileMeta) {

	// Check if file already exists
	_, exists := c.receivedFiles.Load(meta.FileID)
	if exists {
		log.Println("Meta already exists for file:", meta.FileName)
		return
	}

	// Start Creating New File
	wd, err := os.Getwd()
	if err != nil {
		log.Println("Error getting working directory:", err)
		return
	}

	filePath := filepath.Join(wd, fmt.Sprintf("client%d_%s", meta.ClientID, meta.FileName))
	file, err := os.Create(filePath)
	if err != nil {
		log.Println("Error creating file:", err)
		return
	}

	// Start new session for the this file
	session := &models.ReceiveSession{
		FileMeta: meta,
		File:     file,
		Received: 0,
		Chunks:   make(map[uint16]bool),
		// ChunkChan:  make(chan models.FileChunk, 50),
		TotalChunk: uint16((meta.FileSize + uint32(meta.ChunkSize) - 1) / uint32(meta.ChunkSize)),
		AckChan:    make(chan models.AckResponse, 10),
	}

	// Store the session
	c.receivedFiles.Store(meta.FileID, session)

	// Start Req for file chunks
	// c.RequestFileChunks(session)
}

func (c *Client) appendFileChunk(chunk models.FileChunk) {
	val, exists := c.receivedFiles.Load(chunk.FileID)
	if !exists {
		log.Printf("Received chunk for unknown file %d (waiting for meta)\n", chunk.FileID)
		return
	}

	session := val.(*models.ReceiveSession)

	if session.Chunks[chunk.Seq] {
		log.Printf("⚠ Duplicate Seq = %d ignored \n", chunk.Seq)
		return
	}

	// session.AckChan <- models.AckResponse{
	// 	Seq:    chunk.Seq,
	// 	Status: 200, // ReceivedOK
	// }

	session.Chunks[chunk.Seq] = true

	offset := int64(chunk.Seq) * int64(session.ChunkSize)
	session.File.WriteAt(chunk.Data, offset)
	session.Received++
	log.Printf("Recieved From Client%d (%d/%d) seq = %d \n", chunk.ClientID, session.Received, session.TotalChunk, chunk.Seq)

	if session.Received > session.TotalChunk {
		log.Printf("✅ Client%d File %d done (%.2f KB)\n", chunk.ClientID, chunk.FileID, float64(session.TotalChunk))
		session.File.Close()
		// close(session.ChunkChan)
		c.receivedFiles.Delete(chunk.FileID)

		return
	}
}

// Additional Functions
func (c *Client) cleanupSendOutFiles(ttl time.Duration) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		c.sendOutFiles.Range(func(key, value any) bool {
			session := value.(*models.SendSession)
			if now.Sub(session.CreatedAt) > ttl {
				log.Printf("🧹 Cleaning up expired send session (fileID=%d, name=%s)\n", session.FileID, session.FileName)
				session.File.Close()
				c.sendOutFiles.Delete(key)
			}
			return true
		})
	}
}

func (clientID *Client) setupGracfulShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("\n🛑 Shutting down server gracefully...")
		// log.Println("AllocationCnt: ", c.allocationCnt)
		utils.PrintApiLog("Server Exit")
		os.Exit(0)
	}()
}

func (c *Client) updateRTT(rtt time.Duration) {
	// Update the RTT
	if c.smoothedRTT == 0 {
		c.smoothedRTT = rtt
		c.rttVar = rtt / 2
	} else {
		rttDiff := c.smoothedRTT - rtt
		if rttDiff < 0 {
			rttDiff = -rttDiff
		}

		c.rttVar = time.Duration((1-beta)*float64(c.rttVar) + beta*float64(rttDiff))
		c.smoothedRTT = time.Duration((1-alpha)*float64(c.smoothedRTT) + alpha*float64(rtt))
	}

	c.rto = c.smoothedRTT + 4*c.rttVar

}

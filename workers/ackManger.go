package workers

type ackCommand struct {
	action   string
	packetID uint32
	reply    chan interface{}
}

type AckManager struct {
	pending  map[uint32]chan bool
	commands chan ackCommand
}

// NewAckManager creates and starts a new acknowledgment manager goroutine.
func NewAckManager() *AckManager {
	m := &AckManager{
		pending:  make(map[uint32]chan bool),
		commands: make(chan ackCommand),
	}
	go m.run()
	return m
}

func (m *AckManager) run() {
	for cmd := range m.commands {
		switch cmd.action {
		case "add":
			m.pending[cmd.packetID] = make(chan bool, 1)
			if cmd.reply != nil {
				cmd.reply <- true
			}
		case "delete":
			delete(m.pending, cmd.packetID)
			if cmd.reply != nil {
				cmd.reply <- true
			}
		case "get":
			ch, ok := m.pending[cmd.packetID]
			if cmd.reply != nil {
				if ok {
					cmd.reply <- ch
				} else {
					cmd.reply <- nil
				}
			}
		}
	}
}

// AddAck registers a new pending acknowledgment channel for a packet.
func (m *AckManager) AddAck(packetID uint32) {
	m.commands <- ackCommand{action: "add", packetID: packetID}
}

// DeleteAck removes the pending acknowledgment channel for a packet.
func (m *AckManager) DeleteAck(packetID uint32) {
	m.commands <- ackCommand{action: "delete", packetID: packetID}
}

// GetAck retrieves the acknowledgment channel for a given packet, if it exists.
func (m *AckManager) GetAck(packetID uint32) chan bool {
	reply := make(chan interface{})
	m.commands <- ackCommand{action: "get", packetID: packetID, reply: reply}
	if result, ok := (<-reply).(chan bool); ok {
		return result
	}
	return nil
}

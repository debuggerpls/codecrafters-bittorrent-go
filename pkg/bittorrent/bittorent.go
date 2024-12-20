package bittorrent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// TODO: refactor torrent file argument out
func PeerWorker(ctx context.Context, address string, torrent *TorrentFile, todo <-chan *Piece, done chan<- *Piece, errs chan<- error) {
	log.Printf("%s: starting..\n", address)
	// Connection to peer
	conn, err := net.Dial("tcp", address)
	if err != nil {
		errs <- fmt.Errorf("%s: failed to connect: %s", address, err)
		return
	}
	defer conn.Close()

	// FIXME: what is the best way to receive errors?
	handler := NewPeerStateHandler()

	go HandleIncomingMessages(conn, handler.Incoming, handler.Errs)

	infoHash, err := torrent.InfoHash()
	if err != nil {
		errs <- fmt.Errorf("%s: %s", address, err)
		return
	}
	// handshake should be done immediately
	handler.Outgoing <- *NewHandshakeMessage(torrent.Progress.PeerID, infoHash)

	PeerWorkerInitialized(ctx, address, torrent, conn, handler, todo, done, errs)
}

func PeerWorkerInitialized(ctx context.Context, address string, torrent *TorrentFile, conn net.Conn, handler *PeerStateHandler, todo <-chan *Piece, done chan<- *Piece, errs chan<- error) {
	log.Printf("%s: initialized..\n", address)

	// FIXME: should this be protected by mutex?
	var piece *Piece

	// TODO: add keepalive message
	// case <-time.Tick(time.Second * 1):
	for {
		select {
		case <-ctx.Done():
			// FIXME: do we need to clean up here? Or is this always the worst case scenario?
			log.Printf("%s: context was canceled", address)
			return
		case outMsg := <-handler.Outgoing:
			// FIXME: is there a case when didnt send all?
			_, err := outMsg.WriteTo(conn)
			if err != nil {
				errs <- fmt.Errorf("%s: failed to send: %s", address, err)
				return
			}
		case inMsg := <-handler.Incoming:
			// HandleMessage Incoming messages
			outMsg := handler.HandleMessage(&inMsg, piece)
			if outMsg != nil {
				handler.Outgoing <- *outMsg
			}

			// check if everything is downloaded
			if piece.Buffer.Len() == piece.Len {

				err := piece.SaveToFile()
				if err != nil {
					errs <- fmt.Errorf("%s: save fail idx=%d: %s", address, piece.Idx, err)
					return
				}

				piece.Done = true
				done <- piece
				piece = nil
			}

		case err := <-handler.Errs:
			// HandleMessage errors
			errs <- fmt.Errorf("%s: error: %s", address, err)
			if piece != nil {
				piece.Done = false
				done <- piece
				piece = nil
			}

			return
		default:
			// is this a busy loop?
			if piece == nil {
				p, ok := <-todo
				if ok {
					piece = p
					log.Printf("%s: starting downloading piece: idx=%d length=%d buf=%d\n", address, piece.Idx, piece.Len, piece.Buffer.Len())
					// fake keep_alive message so that download begins
					handler.Incoming <- *NewKeepAliveMessage()
				} else {
					time.Sleep(100 * time.Millisecond)
				}
			} else {
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
}

// For single file torrent:
// * consists of multiple pieces
//   * consists of multiple blocks
//
// pieces can be downloaded by multiple peers, BUT for a piece only 1 peer is used
// each peer requires a handshake and so on, which means it requires a state

// possible architecture:
// There is a pool of peers - with their own state (ex. which pieces they have, handshake, peer_id, connected/disconnected)
// There are a pool of required pieces - with state (index, hash, outputFile, state(idle, in_progress, done, error (wrong hash, failed to download, dont have piece)
//

// future improvements: measure download speed and use the fastest peers

// Handshake message constants for version 1.0 of the BitTorrent protocol
const (
	LenHandshakePstrlen     = 1
	LenHandshakePstr        = 19 // protocol
	LenHandshakeReserved    = 8
	LenHandshakeInfoHash    = 20
	LenHandshakePeerId      = 20
	LenHandshakeMsg         = LenHandshakePstrlen + LenHandshakePstr + LenHandshakeReserved + LenHandshakeInfoHash + LenHandshakePeerId
	OffsetHandshakePstrlen  = 0
	OffsetHandshakePstr     = OffsetHandshakePstrlen + LenHandshakePstrlen // protocol
	OffsetHandshakeReserved = OffsetHandshakePstr + LenHandshakePstr
	OffsetHandshakeInfoHash = OffsetHandshakeReserved + LenHandshakeReserved
	OffsetHandshakePeerId   = OffsetHandshakeInfoHash + LenHandshakeInfoHash
)

// Stardard Len for mainline version 4
const (
	LenRequestBlockLength = 16 * 1024
)

// Peer wire message contansts
const (
	LenMsgReq           = 13
	LenMsgLenPrefix     = 4
	LenMsgMessageId     = 1
	LenMsgInteger       = 4
	OffsetMsgLenPrefix  = 0
	OffsetMsgId         = OffsetMsgLenPrefix + LenMsgLenPrefix
	OffsetMsgReqIndex   = OffsetMsgId + LenMsgMessageId
	OffsetMsgReqBegin   = OffsetMsgReqIndex + LenMsgInteger
	OffsetMsgReqLength  = OffsetMsgReqBegin + LenMsgInteger
	OffsetMsgPieceIndex = OffsetMsgReqIndex
	OffsetMsgPieceBegin = OffsetMsgReqBegin
	OffsetMsgPieceBlock = OffsetMsgReqLength
)

type TorrentFile struct {
	FilePath string
	Announce string
	Info     TorrentFileInfo
	Progress TorrentProgress
}

type TorrentFileInfo struct {
	Length      int
	Name        string
	PieceLength int
	Pieces      string
}

type TorrentProgress struct {
	PeerID     [20]byte
	Port       int
	Uploaded   int
	Downloaded int
	Left       int
	Compact    int
}

func getInfoValue[T comparable](info map[string]interface{}, key string, valueType T) (T, error) {
	value, ok := info[key]
	if !ok {
		return valueType, fmt.Errorf("info.%s: no \"%s\" field, map=%v", key, key, info)
	}
	val, ok := value.(T)
	if !ok {
		return valueType, fmt.Errorf("info.%s: invalid \"%s\" field, map=%v", key, key, info)
	}
	return val, nil
}

func (torrent *TorrentFile) InfoHash() ([20]byte, error) {
	// because we know that torrent file is valid, we can just use the file itself
	data, err := os.ReadFile(torrent.FilePath)
	if err != nil {
		return [20]byte{}, err
	}

	// 4:infod<INFO_CONTENTS>e
	infoStart := bytes.Index(data, []byte("4:info")) + 6
	if infoStart < 0 {
		return [20]byte{}, fmt.Errorf("TorrentFile.info: no info in torrent file")
	}
	return sha1.Sum(data[infoStart : len(data)-1]), nil
}

func NewTorrentFile(filePath string, port int) (*TorrentFile, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stat.Size()
	torrent := &TorrentFile{FilePath: filePath}

	buf := make([]byte, stat.Size())
	size, err := f.Read(buf)
	if err != nil {
		return nil, err
	}
	if len(buf) != size {
		return nil, fmt.Errorf("did not read full torrent file, file Len: %d, read: %d", size, len(buf))
	}

	d, _, err := DecodeBencodeDict(string(buf))
	if err != nil {
		return nil, err
	}

	value, ok := d["announce"]
	if !ok {
		return nil, fmt.Errorf("\"announce\" not found in torrent file")
	}
	announce, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("\"announce\" in torrent file is not BencodeString")
	}
	torrent.Announce = announce

	value, ok = d["info"]
	if !ok {
		return nil, fmt.Errorf("\"info\" not found in torrent file")
	}
	info, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("\"info\" in torrent file is not BencodeDict")
	}

	torrent.Info.Length, err = getInfoValue(info, "length", torrent.Info.Length)
	if err != nil {
		return nil, err
	}

	torrent.Info.Name, err = getInfoValue(info, "name", torrent.Info.Name)
	if err != nil {
		return nil, err
	}

	torrent.Info.PieceLength, err = getInfoValue(info, "piece length", torrent.Info.PieceLength)
	if err != nil {
		return nil, err
	}

	torrent.Info.Pieces, err = getInfoValue(info, "pieces", torrent.Info.Pieces)
	if err != nil {
		return nil, err
	}

	torrent.Progress.Compact = 1
	torrent.Progress.Port = port
	_, err = rand.Read(torrent.Progress.PeerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to generate peer_id: %s\n", err)
	}

	return torrent, nil
}

func (torrent *TorrentFile) newTrackerRequestURL() (string, error) {
	infoHash, err := torrent.InfoHash()
	if err != nil {
		return "", err
	}

	trackerParams := url.Values{}
	trackerParams.Set("info_hash", string(infoHash[:]))
	trackerParams.Set("peer_id", string(torrent.Progress.PeerID[:]))
	trackerParams.Set("port", strconv.Itoa(torrent.Progress.Port))
	trackerParams.Set("uploaded", strconv.Itoa(torrent.Progress.Uploaded))
	trackerParams.Set("downloaded", strconv.Itoa(torrent.Progress.Downloaded))
	// TODO: this should be calculated in the future
	trackerParams.Set("left", strconv.Itoa(torrent.Info.Length))
	trackerParams.Set("compact", strconv.Itoa(torrent.Progress.Compact))

	trackerRequestURL := fmt.Sprintf("%s?%s", torrent.Announce, trackerParams.Encode())
	return trackerRequestURL, nil
}

func (torrent *TorrentFile) GetTrackerResponse() (*TrackerResponse, error) {
	trackerRequestURL, err := torrent.newTrackerRequestURL()
	if err != nil {
		return nil, err
	}
	//fmt.Println(trackerRequestURL)
	resp, err := http.Get(trackerRequestURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	trackerResponse, _, err := DecodeBencodeDict(string(body))
	if err != nil {
		return nil, err
	}

	response := &TrackerResponse{Peers: make([]TrackerPeer, 0)}
	peers, err := getInfoValue(trackerResponse, "peers", "")
	if err != nil {
		return nil, err
	}

	for i := 0; i < len(peers); i += 6 {
		response.Peers = append(response.Peers, TrackerPeer{Ip: net.IPv4(peers[i], peers[i+1], peers[i+2], peers[i+3]), Port: int(binary.BigEndian.Uint16([]byte(peers[i+4:])))})
	}

	// optional
	response.Interval, err = getInfoValue(trackerResponse, "interval", response.Interval)
	if err != nil {
		response.Interval = -1
	}

	return response, nil
}

type TrackerResponse struct {
	Interval int
	Peers    []TrackerPeer
}

type TrackerPeer struct {
	Ip   net.IP
	Port int
}

func (peer TrackerPeer) String() string {
	return fmt.Sprintf("%s:%d", peer.Ip, peer.Port)
}

type MessageType uint8

const (
	CHOKE MessageType = iota
	UNCHOKE
	INTERESTED
	NOT_INTERESTED
	HAVE
	BITFIELD
	REQUEST
	PIECE
	CANCEL
	PORT
	EXTENDED   MessageType = 20
	KEEP_ALIVE MessageType = 100
	HANDSHAKE  MessageType = 101
	INVALID    MessageType = 102
)

var MessageTypeNames = map[MessageType]string{
	CHOKE:          "CHOKE",
	UNCHOKE:        "UNCHOKE",
	INTERESTED:     "INTERESTED",
	NOT_INTERESTED: "NOT_INTERESTED",
	HAVE:           "HAVE",
	BITFIELD:       "BITFIELD",
	REQUEST:        "REQUEST",
	PIECE:          "PIECE",
	CANCEL:         "CANCEL",
	PORT:           "PORT",
	EXTENDED:       "EXTENDED",
	KEEP_ALIVE:     "KEEP_ALIVE",
	HANDSHAKE:      "HANDSHAKE",
	INVALID:        "INVALID",
}

const (
	LEN_PREFIX               = 4
	LEN_MESSAGE_ID           = 1
	LEN_MESSAGE_INDEX        = 4
	LEN_MESSAGE_BEGIN        = 4
	LEN_PIECE_BLOCK_STANDARD = 16 * 1024
	LEN_MESSAGE_MAX          = LEN_PREFIX + LEN_MESSAGE_ID + LEN_MESSAGE_INDEX + LEN_MESSAGE_BEGIN + LEN_PIECE_BLOCK_STANDARD
	LEN_HANDSHAKE            = 68
)

const (
	OFF_EXTENDED_MSG_ID = LEN_PREFIX + LEN_MESSAGE_ID
	OFF_EXTENDED_DICT   = OFF_EXTENDED_MSG_ID + LEN_MESSAGE_ID
)

func (t MessageType) String() string {
	return MessageTypeNames[t]
}

var (
	ErrBufferTooSmall = fmt.Errorf("buffer is too small")
)

type Message struct {
	Data []byte
	Len  int
}

func (m *Message) Type() MessageType {
	if m.Len < LEN_PREFIX {
		//log.Printf("m.Len < LEN_PREFIX\n")
		return INVALID
	}

	// FIXME: this might not always be the case
	if m.Data[0] == 19 && m.Len == LEN_HANDSHAKE {
		//log.Printf("m.Data[0] == 19 && m.Len == LEN_HANDSHAKE \n")
		return HANDSHAKE
	}

	length := binary.BigEndian.Uint32(m.Data[0:LEN_PREFIX])
	if m.Len != int(length)+LEN_PREFIX {
		//log.Printf("%d != %d+%d\n", m.Len, int(length), LEN_PREFIX)
		return INVALID
	}

	if length == 0 {
		//log.Printf("length == 0\n")
		return KEEP_ALIVE
	}

	mType := m.Data[LEN_PREFIX]
	// FIXME: it is very unlikely that the message id is not valid
	return MessageType(mType)
}

func (m *Message) Read(b []byte) (n int, err error) {
	if m.Len > len(b) {
		return 0, ErrBufferTooSmall
	}

	copy(b, m.Data[:m.Len])
	// FIXME: do we need to do this?
	m.Len = 0
	return m.Len, nil
}

func MessageFromBytes(b []byte) Message {
	// FIXME: if we assign it in constructor, is it copied automatically?
	m := Message{
		Data: make([]byte, len(b)),
		Len:  len(b),
	}
	copy(m.Data, b)

	//log.Printf("MessageFromBytes: type=%s Len=%d", m.Type(), m.Len)

	return m
}

func (m *Message) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(m.Data[:m.Len])
	m.Len -= n
	return int64(n), err
}

func ContainsMessage(b []byte) (bool, int) {
	if len(b) < LEN_PREFIX {
		return false, 0
	}

	// FIXME: this might not always be the case
	if b[0] == 19 && len(b) >= LEN_HANDSHAKE {
		return true, LEN_HANDSHAKE
	}

	length := int(binary.BigEndian.Uint32(b[0:LEN_PREFIX]))

	if len(b) < length+LEN_PREFIX {
		return false, 0
	}

	return true, LEN_PREFIX + length
}

type Piece struct {
	Idx      int
	Len      int
	Done     bool
	Path     string
	Hash     [20]byte
	PeerId   [20]byte
	InfoHash [20]byte
	Buffer   bytes.Buffer
}

func (piece *Piece) SaveToFile() error {
	receivedHash := sha1.Sum(piece.Buffer.Bytes())

	if receivedHash != piece.Hash {
		return fmt.Errorf("hash mismatch: expected %x, received %x", piece.Hash, receivedHash)
	}
	output, err := os.Create(piece.Path)
	if err != nil {
		return fmt.Errorf("failed to create output file: %s\n", err)
	}
	defer output.Close()

	if _, err = output.Write(piece.Buffer.Bytes()); err != nil {
		return fmt.Errorf("failed to create output file: %s\n", err)
	}

	return nil
}

type PeerStateHandler struct {
	Outgoing  chan Message
	Incoming  chan Message
	Errs      chan error
	PeerState *PeerState
}

func NewPeerStateHandler() *PeerStateHandler {
	return &PeerStateHandler{
		Outgoing:  make(chan Message, 10),
		Incoming:  make(chan Message, 10),
		Errs:      make(chan error, 2),
		PeerState: NewPeerState(),
	}
}

// HandleMessage should be called only AFTER the Handshake message was sent!
// TODO: we need to pass Piece information so that HandleMessage() can ask for blocks?
func (handler *PeerStateHandler) HandleMessage(msg *Message, piece *Piece) *Message {
	// do handshake first
	// TODO: when using iota you need default case?!
	//log.Printf("Handling message type: %s", msg.Type())
	switch t := msg.Type(); t {
	case HANDSHAKE:
		handler.PeerState.Done_handshake = true
	case UNCHOKE:
		handler.PeerState.peer_choking = false
	case CHOKE:
		handler.PeerState.peer_choking = true
	case PIECE:
		if piece != nil {
			// add to the buffer
			//log.Printf("HandleMessage: received piece - cur=%d recv=%d", piece.Buffer.Len(), Len(msg.AsPiece().Block()))

			piece.Buffer.Write(msg.AsPiece().Block())
		} else {
			//log.Printf("HandleMessage: received %q but no active piece!", t)
			return nil
		}
	}

	// TODO: should send these messages only when handshake is done
	if !handler.PeerState.Done_handshake {
		//panic("Handshake message was not received!")
		// FIXME: is it ok to wait for handshake?
		return nil
	}

	if piece == nil {
		// TODO: here we should set ourselves not_interested
		return nil
	} else {
		// TODO: what to do if peer_choking?
		if !handler.PeerState.am_interested {
			handler.PeerState.am_interested = true
			//log.Printf("Sending interested")
			return NewInterestedMessage()
		}

		// TODO: is this ok to wait for unchoke? or should we initiate ourselves?
		if handler.PeerState.peer_choking {
			return nil
		}

		if piece.Buffer.Len() < piece.Len {
			blockLength := LEN_PIECE_BLOCK_STANDARD
			if piece.Buffer.Len()+blockLength > piece.Len {
				blockLength = piece.Len - piece.Buffer.Len()
			}

			//log.Printf("sending request: idx=%d begin=%d length=%d", piece.Idx, piece.Buffer.Len(), blockLength)

			return NewRequestMessage(piece.Idx, piece.Buffer.Len(), blockLength)
		}
	}

	return nil
}

type PeerState struct {
	Done_handshake  bool
	am_choking      bool
	am_interested   bool
	peer_choking    bool
	peer_interested bool
}

func NewPeerState() *PeerState {
	return &PeerState{
		am_choking:   true,
		peer_choking: true,
	}
}

func HandleIncomingMessages(conn net.Conn, in chan<- Message, errs chan<- error) {
	// FIXME: context is required here for graceful shutdown
	buf := make([]byte, LEN_MESSAGE_MAX)
	tail := 0
	for {
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))

		n, err := conn.Read(buf[tail:])
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				// just retry
				continue
			}
			errs <- fmt.Errorf("read err: %s", err)
			return
		}
		tail += n

		//log.Printf("Incoming message: tail=%d", tail)

		// TODO: this is very inefficient
		head := 0
		for head < tail {
			ok, length := ContainsMessage(buf[head:tail])
			if !ok {
				// if were not at the start, we need to copy
				//log.Printf("No msg: head=%d tail=%d", head, tail)
				if head != 0 {
					copy(buf, buf[head:tail])
				}
				tail -= head
				// wait for more Data
				break
			}
			//log.Printf("containsMessage, head=%d, tail=%d, Len=%d", head, tail, length)
			in <- MessageFromBytes(buf[head : head+length])
			head += length
		}

		if head == tail {
			tail = 0
		}
	}
}

func NewHandshakeMessage(peerId [20]byte, infoHash [20]byte) *Message {
	msg := &Message{
		Data: make([]byte, LEN_HANDSHAKE),
		Len:  LEN_HANDSHAKE,
	}

	msg.Data[OffsetHandshakePstrlen] = 19
	copy(msg.Data[OffsetHandshakePstr:], "BitTorrent protocol")
	copy(msg.Data[OffsetHandshakeReserved:], make([]byte, 8))
	copy(msg.Data[OffsetHandshakeInfoHash:], infoHash[:])
	copy(msg.Data[OffsetHandshakePeerId:], peerId[:])

	return msg
}

func NewInterestedMessage() *Message {
	msg := &Message{
		Data: []byte{0, 0, 0, 1, byte(INTERESTED)},
		Len:  5,
	}

	return msg
}

func NewRequestMessage(index, begin, length int) *Message {
	const (
		offsetIndex  = LEN_PREFIX + LEN_MESSAGE_ID
		offsetBegin  = offsetIndex + 4
		offsetLength = offsetBegin + 4
		msgLength    = offsetLength + 4
	)

	msg := &Message{
		Data: make([]byte, msgLength),
		Len:  msgLength,
	}
	// Len=0013
	msg.Data[3] = msgLength - 4
	msg.Data[4] = byte(REQUEST)
	binary.BigEndian.PutUint32(msg.Data[offsetIndex:], uint32(index))
	binary.BigEndian.PutUint32(msg.Data[offsetBegin:], uint32(begin))
	binary.BigEndian.PutUint32(msg.Data[offsetLength:], uint32(length))

	return msg
}

func NewKeepAliveMessage() *Message {
	return &Message{
		Data: make([]byte, 4),
		Len:  4,
	}
}

func NewExtendedMessage() *ExtendedMessage {
	msg := Message{
		Data: []byte{0, 0, 0, 2, byte(EXTENDED), 0},
		Len:  6,
	}

	return &ExtendedMessage{Message: msg}
}

type PieceMessage struct {
	Message
}

// TODO: implement other methods
func (p *PieceMessage) Block() []byte {
	const (
		index = LEN_PREFIX + LEN_MESSAGE_ID
		begin = index + 4
		block = begin + 4
	)

	return p.Data[block:p.Len]
}

func (m *Message) AsPiece() *PieceMessage {
	return &PieceMessage{Message: *m}
}

type HandshakeMessage struct {
	Message
}

func (handshake *HandshakeMessage) SetExtensions() {
	const offsetExtensionByte = OffsetHandshakeReserved + 5
	handshake.Data[offsetExtensionByte] = 0x10
}

func (handshake *HandshakeMessage) HasExtensions() bool {
	const offsetExtensionByte = OffsetHandshakeReserved + 5
	return (handshake.Data[offsetExtensionByte] & 0x10) == 0x10
}

func (handshake *HandshakeMessage) PeerId() [20]byte {
	return [20]byte(handshake.Data[OffsetHandshakePeerId : OffsetHandshakePeerId+20])
}

func (m *Message) AsHandshake() *HandshakeMessage {
	return &HandshakeMessage{Message: *m}
}

type ExtendedMessage struct {
	Message
}

func (m *Message) AsExtended() *ExtendedMessage {
	return &ExtendedMessage{Message: *m}
}

func (m *ExtendedMessage) ExtensionMessageId() byte {
	return m.Data[OFF_EXTENDED_MSG_ID]
}

func (m *ExtendedMessage) SetExtensionMessageId(id byte) {
	m.Data[OFF_EXTENDED_MSG_ID] = id
}

func (m *ExtendedMessage) IsHandshake() bool {
	return m.ExtensionMessageId() == 0
}

func (m *ExtendedMessage) ExtensionDict() []byte {
	return m.Data[OFF_EXTENDED_DICT:m.Len]
}

// TODO: why here I need to give the pointer back, otherwise no changes are visible after calling it?
func (m *ExtendedMessage) AddDict(d map[string]interface{}) *ExtendedMessage {
	encodedD := BencodeDict(d)
	m.Len = 6 + len(encodedD)
	//fmt.Printf("Encoded Len: %d\n", m.Len)
	// FIXME: this is where the slice is changed, thus not being a reference anymore to the original
	m.Data = append(m.Data, encodedD...)

	//decoded, err := DecodeBencode(string(m.ExtensionDict()))
	//if err != nil {
	//	panic("Failed to decode dict:" + err.Error())
	//}
	//fmt.Printf("Encoded l=%d s: %q\n", Len(encodedD), encodedD)
	//fmt.Printf("Encoded dict: %q\n", decoded)
	binary.BigEndian.PutUint32(m.Data, uint32(2+len(encodedD)))
	//fmt.Printf("%x ", m.Data)
	//fmt.Printf("Encoded: %x \n", m.Data)

	return m
}

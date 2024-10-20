package bittorrent

import (
	"bufio"
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

	go HandleIncomingMessages(conn, handler.incoming, handler.errs)
	infoHash, err := torrent.InfoHash()
	if err != nil {
		errs <- fmt.Errorf("%s: %s", address, err)
		return
	}
	// handshake should be done immediately
	handler.outgoing <- *NewHandshakeMessage(torrent.Progress.PeerID, infoHash)

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
		case outMsg := <-handler.outgoing:
			// FIXME: is there a case when didnt send all?
			_, err := outMsg.WriteTo(conn)
			if err != nil {
				errs <- fmt.Errorf("%s: failed to send: %s", address, err)
				return
			}
		case inMsg := <-handler.incoming:
			// HandleMessage incoming messages
			outMsg := handler.HandleMessage(&inMsg, piece)
			if outMsg != nil {
				handler.outgoing <- *outMsg
			}

			// check if everything is downloaded
			if piece.Buffer.Len() == piece.Len {

				err = piece.SaveToFile()
				if err != nil {
					errs <- fmt.Errorf("%s: save fail idx=%d: %s", address, piece.Idx, err)
					return
				}

				piece.Done = true
				done <- piece
				piece = nil
			}

		case err := <-handler.errs:
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
					handler.incoming <- *NewKeepAliveMessage()
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

// implementation
// - peers are a worker group - by default - keep alive tick every 2 mins
// - pieces are the work items - they are provided by channel
//    - when item is finished, upper loop is notified by sending the work item back with updated state
//    -
// - peers receive an item and start downloading its blocks, when all done

func downloadPiece(torrent *TorrentFile, rw *bufio.ReadWriter, msg *PeerWireMessage, piece *Piece) error {
	var err error

	// TODO: do not handshake if its already done
	// Handshake
	if err = torrent.FillHandshakeMessage(msg); err != nil {
		return err
	}

	if err = HandlePeerWireProtocol(rw, msg); err != nil {
		return fmt.Errorf("failed handshake: %s\n", err)
	}

	// no more handshake messages from here on
	msg.Handshake = false

	log.Printf("Peer ID: %x\n", msg.Buffer[OffsetHandshakePeerId:LenHandshakeMsg])

	log.Printf("Starting PieceIndex=%d\n", piece.Idx)

	// Step 1: wait for bitfield message from the peer
	// The bitfield message may only be sent immediately after the handshaking sequence is completed,
	// and BEFORE any other messages are sent.
	// It is optional, and need not be sent if a client has no pieces.
	receivedBitfield := false
	if msg.Len > LenHandshakeMsg {
		for offset := LenHandshakeMsg; offset < msg.Len; {
			lenPrefix := msg.ToInt(offset)
			switch {
			case lenPrefix == 0:
			//	fmt.Println("received \"keep-alive\" peer message after handshake")
			default:
				//fmt.Printf("received \"%s\" peer message after handshake\n", msg.ToMsgId(offset+4))
				// TODO: implement bitfield checks
				receivedBitfield = true
			}

			offset += 4 + lenPrefix
		}
	}

	for !receivedBitfield {
		// just send a keep-alive until we receive bitfield
		clear(msg.Buffer[0:4])
		msg.Len = 4
		if err = HandlePeerWireProtocol(rw, msg); err != nil {
			return fmt.Errorf("failed receiving \"bitfield\" peer message: %s\n", err)
		}

		for offset := 0; offset < msg.Len; {
			lenPrefix := msg.ToInt(offset)

			switch {
			case lenPrefix == 0:
				//fmt.Println("received \"keep-alive\" peer message")
			default:
				//fmt.Printf("received \"%s\" peer message\n", msg.ToMsgId(offset+4))
				// TODO: implement bitfield checks
				receivedBitfield = true
			}

			offset += 4 + lenPrefix
		}
	}

	// Step 2: send an interested message
	// Step 3: wait for unchoke message
	for receivedUnchoke := false; !receivedUnchoke; {
		copy(msg.Buffer, []byte{0, 0, 0, 1, byte(INTERESTED)})
		msg.Len = 5
		if err = HandlePeerWireProtocol(rw, msg); err != nil {
			return fmt.Errorf("failed receiving \"unchoke\" peer message: %s\n", err)
		}
		for offset := 0; offset < msg.Len; {
			lenPrefix := msg.ToInt(offset)
			switch {
			case lenPrefix == 0:
				//fmt.Println("received \"keep-alive\" peer message")
			default:
				//fmt.Printf("received \"%s\" peer message\n", msg.ToMsgId(offset+4))
				if msg.ToMsgId(offset+4) == UNCHOKE {
					receivedUnchoke = true
				}
			}

			offset += 4 + lenPrefix
		}
	}

	for piece.Buffer.Len() < piece.Len {
		blockLength := LenRequestBlockLength
		if piece.Buffer.Len()+blockLength > piece.Len {
			blockLength = piece.Len - piece.Buffer.Len()
		}

		// FIXME: error handling
		_ = torrent.FillRequestMessage(msg, piece.Idx, piece.Buffer.Len(), blockLength)

		if err = HandlePeerWireProtocol(rw, msg); err != nil {
			return fmt.Errorf("failed receiving \"%s\" peer message, recv=%d: %s\n", REQUEST, piece.Buffer.Len(), err)
		}

		// HandleMessage received messages
		for offset := 0; offset < msg.Len; {
			lenPrefix := msg.ToInt(offset)
			switch {
			case lenPrefix == 0:
				//fmt.Println("received \"keep-alive\" peer message")
			default:
				msgId := msg.ToMsgId(offset + OffsetMsgId)
				//fmt.Printf("received \"%s\" peer message\n", msgId)

				if msgId == PIECE {
					//fmt.Printf("pieceBufferLen=%d , adding=%d\n", pieceBuffer.Len(), LenMsgLenPrefix+lenPrefix-OffsetMsgPieceBlock)
					piece.Buffer.Write(msg.Buffer[offset+OffsetMsgPieceBlock : offset+LenMsgLenPrefix+lenPrefix])
				}
			}

			offset += LenMsgLenPrefix + lenPrefix
		}
	}

	log.Printf("Downloaded pieceIndex=%d: length=%d, expected=%d\n", piece.Idx, piece.Buffer.Len(), piece.Len)

	// Step 5: check piece hash
	receivedHash := sha1.Sum(piece.Buffer.Bytes())

	var expectedHash [sha1.Size]byte
	copy(expectedHash[:], torrent.Info.Pieces[piece.Idx*20:piece.Idx*20+20])
	if receivedHash != expectedHash {
		fmt.Printf("expected piece hash != received piece hash; pieceIndex=%d!\n", piece.Idx)
		fmt.Printf("received piece hash: %x\n", receivedHash)
		fmt.Printf("expected piece hash: %x\n", expectedHash)
		fmt.Printf("full     piece hash: %x\n", torrent.Info.Pieces)
		return fmt.Errorf("expected piece hash != received piece hash; pieceIndex=%d!\n", piece.Idx)
	}
	output, err := os.Create(piece.Path)
	if err != nil {
		return fmt.Errorf("failed to create output file: %s\n", err)
	}
	defer output.Close()

	outputWriter := bufio.NewWriter(output)
	if _, err = outputWriter.Write(piece.Buffer.Bytes()); err != nil {
		return fmt.Errorf("failed to create output file: %s\n", err)
	}
	if err = outputWriter.Flush(); err != nil {
		return fmt.Errorf("failed to flush to output file: %s\n", err)
	}

	// FIXME: should this be done here or outside?
	piece.Done = true

	return nil
}

// This structure is used for PeerWireProtocol, both for sending and receiving messages
// it contains a Buffer that contains the message that should be sent and then is used
// for receiving data. If handshake bool is set, then it is a handshake, otherwise
// it will wait to receive all bytes in the length prefix
// if len is <0 , then no message will be sent, only waited for
type PeerWireMessage struct {
	Buffer    []byte
	Len       int
	Handshake bool
}

// decode bigEndian uint32 from Buffer index
func (msg *PeerWireMessage) ToInt(index int) int {
	return int(binary.BigEndian.Uint32(msg.Buffer[index:]))
}

// convert byte to PeerWireMessageId at Buffer index
func (msg *PeerWireMessage) ToMsgId(index int) MessageType {
	return MessageType(msg.Buffer[index])
}

// HandlePeerWireProtocol
// Note: provided message must ensure that there is enough space in the Buffer
// Note: following handshake response, there might be peer messages in the same Buffer
// TODO: check for keep-alive messages, so that they dont overflow the Buffer
func HandlePeerWireProtocol(rw *bufio.ReadWriter, msg *PeerWireMessage) error {
	if msg.Len > 0 {
		_, err := rw.Write(msg.Buffer[:msg.Len])
		if err != nil {
			return errors.New("failed to write message to TCP writer: " + err.Error())
		}
		if err = rw.Flush(); err != nil {
			return errors.New("failed to flush TCP writer: " + err.Error())
		}
	}

	var err error
	msg.Len, err = rw.Read(msg.Buffer)
	if err != nil {
		if err == io.EOF {
			return err
		}
		return errors.New("failed to read message from TCP reader: " + err.Error())
	}
	if msg.Len < 4 {
		// this applies also to handshake messages
		return errors.New("received peer message is too short")
	}
	if msg.Handshake {
		// handshake messages do not have length-prefix
		return nil
	}

	lengthPrefix := int(binary.BigEndian.Uint32(msg.Buffer))
	for msg.Len < lengthPrefix+4 {
		received, err := rw.Read(msg.Buffer[msg.Len:])
		if err != nil {
			if err == io.EOF {
				return err
			} else {
				return errors.New("failed to read message from TCP reader: " + err.Error())
			}
		}

		msg.Len += received
	}

	if msg.Len > lengthPrefix+4 {
		return fmt.Errorf("received peer message is too long: %d", msg.Len)
	}

	return nil
}

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

// Stardard len for mainline version 4
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
		return valueType, fmt.Errorf("TorrentFile.info.%s: no \"%s\" field in torrent file", key, key)
	}
	val, ok := value.(T)
	if !ok {
		return valueType, fmt.Errorf("TorrentFile.info.%s: invalid \"%s\" field in torrent file", key, key)
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
		return nil, fmt.Errorf("did not read full torrent file, file len: %d, read: %d", size, len(buf))
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

	response.Interval, err = getInfoValue(trackerResponse, "interval", response.Interval)
	if err != nil {
		return nil, err
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

func (torrent *TorrentFile) FillHandshakeMessage(msg *PeerWireMessage) error {
	infoHash, err := torrent.InfoHash()
	if err != nil {
		return fmt.Errorf("FillHandshakeMessage: failed to generate info_hash: %s", err)
	}

	msg.Buffer[OffsetHandshakePstrlen] = 19
	copy(msg.Buffer[OffsetHandshakePstr:], "BitTorrent protocol")
	copy(msg.Buffer[OffsetHandshakeReserved:], make([]byte, 8))
	copy(msg.Buffer[OffsetHandshakeInfoHash:], infoHash[:])
	copy(msg.Buffer[OffsetHandshakePeerId:], torrent.Progress.PeerID[:])

	msg.Len = LenHandshakeMsg
	msg.Handshake = true

	return nil
}

func (torrent *TorrentFile) FillRequestMessage(msg *PeerWireMessage, pieceIndex, begin, length int) error {
	clear(msg.Buffer[:LenMsgLenPrefix+LenMsgReq])
	msg.Buffer[3] = LenMsgReq
	msg.Buffer[OffsetMsgId] = byte(REQUEST)
	binary.BigEndian.PutUint32(msg.Buffer[OffsetMsgReqIndex:], uint32(pieceIndex))
	binary.BigEndian.PutUint32(msg.Buffer[OffsetMsgReqBegin:], uint32(begin))
	binary.BigEndian.PutUint32(msg.Buffer[OffsetMsgReqLength:], uint32(length))

	msg.Len = LenMsgLenPrefix + LenMsgReq
	msg.Handshake = false

	return nil
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

func (t MessageType) String() string {
	return MessageTypeNames[t]
}

var (
	ErrBufferTooSmall = fmt.Errorf("buffer is too small")
)

// There are 2 types of peer wire packages
//  1. handshakes - have 19 as first byte, just support
//  2. peer messages - have length as first byte
//     keep-alive <len=0000>
type Message struct {
	data []byte
	len  int
}

func (m *Message) Type() MessageType {
	if m.len < LEN_PREFIX {
		//log.Printf("m.len < LEN_PREFIX\n")
		return INVALID
	}

	// FIXME: this might not always be the case
	if m.data[0] == 19 && m.len == LEN_HANDSHAKE {
		//log.Printf("m.data[0] == 19 && m.len == LEN_HANDSHAKE \n")
		return HANDSHAKE
	}

	length := binary.BigEndian.Uint32(m.data[0:LEN_PREFIX])
	if m.len != int(length)+LEN_PREFIX {
		//log.Printf("%d != %d+%d\n", m.len, int(length), LEN_PREFIX)
		return INVALID
	}

	if length == 0 {
		//log.Printf("length == 0\n")
		return KEEP_ALIVE
	}

	mType := m.data[LEN_PREFIX]
	// FIXME: it is very unlikely that the message id is not valid
	return MessageType(mType)
}

func (m *Message) Read(b []byte) (n int, err error) {
	if m.len > len(b) {
		return 0, ErrBufferTooSmall
	}

	copy(b, m.data[:m.len])
	// FIXME: do we need to do this?
	m.len = 0
	return m.len, nil
}

func MessageFromBytes(b []byte) Message {
	// FIXME: if we assign it in constructor, is it copied automatically?
	m := Message{
		data: make([]byte, len(b)),
		len:  len(b),
	}
	copy(m.data, b)

	//log.Printf("MessageFromBytes: type=%s len=%d", m.Type(), m.len)

	return m
}

func (m *Message) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(m.data[:m.len])
	m.len -= n
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
	outgoing  chan Message
	incoming  chan Message
	errs      chan error
	peerState *PeerState
}

func NewPeerStateHandler() *PeerStateHandler {
	return &PeerStateHandler{
		outgoing:  make(chan Message, 10),
		incoming:  make(chan Message, 10),
		errs:      make(chan error, 2),
		peerState: NewPeerState(),
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
		handler.peerState.done_handshake = true
	case UNCHOKE:
		handler.peerState.peer_choking = false
	case CHOKE:
		handler.peerState.peer_choking = true
	case PIECE:
		if piece != nil {
			// add to the buffer
			//log.Printf("HandleMessage: received piece - cur=%d recv=%d", piece.Buffer.Len(), len(msg.AsPiece().Block()))

			piece.Buffer.Write(msg.AsPiece().Block())
		} else {
			//log.Printf("HandleMessage: received %q but no active piece!", t)
			return nil
		}
	}

	// TODO: should send these messages only when handshake is done
	if !handler.peerState.done_handshake {
		//panic("Handshake message was not received!")
		// FIXME: is it ok to wait for handshake?
		return nil
	}

	if piece == nil {
		// TODO: here we should set ourselves not_interested
		return nil
	} else {
		// TODO: what to do if peer_choking?
		if !handler.peerState.am_interested {
			handler.peerState.am_interested = true
			//log.Printf("Sending interested")
			return NewInterestedMessage()
		}

		// TODO: is this ok to wait for unchoke? or should we initiate ourselves?
		if handler.peerState.peer_choking {
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
	done_handshake  bool
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
				// wait for more data
				break
			}
			//log.Printf("containsMessage, head=%d, tail=%d, len=%d", head, tail, length)
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
		data: make([]byte, LEN_HANDSHAKE),
		len:  LEN_HANDSHAKE,
	}

	msg.data[OffsetHandshakePstrlen] = 19
	copy(msg.data[OffsetHandshakePstr:], "BitTorrent protocol")
	copy(msg.data[OffsetHandshakeReserved:], make([]byte, 8))
	copy(msg.data[OffsetHandshakeInfoHash:], infoHash[:])
	copy(msg.data[OffsetHandshakePeerId:], peerId[:])

	return msg
}

func NewInterestedMessage() *Message {
	msg := &Message{
		data: []byte{0, 0, 0, 1, byte(INTERESTED)},
		len:  5,
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
		data: make([]byte, msgLength),
		len:  msgLength,
	}
	// len=0013
	msg.data[3] = msgLength - 4
	msg.data[4] = byte(REQUEST)
	binary.BigEndian.PutUint32(msg.data[offsetIndex:], uint32(index))
	binary.BigEndian.PutUint32(msg.data[offsetBegin:], uint32(begin))
	binary.BigEndian.PutUint32(msg.data[offsetLength:], uint32(length))

	return msg
}

func NewKeepAliveMessage() *Message {
	return &Message{
		data: make([]byte, 4),
		len:  4,
	}
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

	return p.data[block:p.len]
}

func (m *Message) AsPiece() *PieceMessage {
	return &PieceMessage{Message: *m}
}

type HandshakeMessage struct {
	Message
}

func (handshake *HandshakeMessage) PeerId() [20]byte {
	return [20]byte(handshake.data[OffsetHandshakePeerId : OffsetHandshakePeerId+20])
}

func (m *Message) AsHandshake() *HandshakeMessage {
	return &HandshakeMessage{Message: *m}
}

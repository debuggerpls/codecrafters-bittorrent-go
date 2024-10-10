package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/codecrafters-io/bittorrent-starter-go/bencode"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
)

// Ensures gofmt doesn't remove the "os" encoding/json import (feel free to remove this!)
var _ = json.Marshal

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

	d, _, err := bencode.DecodeBencodeDict(string(buf))
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

	trackerResponse, _, err := bencode.DecodeBencodeDict(string(body))
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

	msg.buffer[OffsetHandshakePstrlen] = 19
	copy(msg.buffer[OffsetHandshakePstr:], "BitTorrent protocol")
	copy(msg.buffer[OffsetHandshakeReserved:], make([]byte, 8))
	copy(msg.buffer[OffsetHandshakeInfoHash:], infoHash[:])
	copy(msg.buffer[OffsetHandshakePeerId:], torrent.Progress.PeerID[:])

	msg.len = LenHandshakeMsg
	msg.handshake = true

	return nil
}

func (torrent *TorrentFile) FillRequestMessage(msg *PeerWireMessage, pieceIndex, begin, length int) error {
	clear(msg.buffer[:LenMsgLenPrefix+LenMsgReq])
	msg.buffer[3] = LenMsgReq
	msg.buffer[OffsetMsgId] = byte(Request)
	binary.BigEndian.PutUint32(msg.buffer[OffsetMsgReqIndex:], uint32(pieceIndex))
	binary.BigEndian.PutUint32(msg.buffer[OffsetMsgReqBegin:], uint32(begin))
	binary.BigEndian.PutUint32(msg.buffer[OffsetMsgReqLength:], uint32(length))

	msg.len = LenMsgLenPrefix + LenMsgReq
	msg.handshake = false

	return nil
}

func main() {
	command := os.Args[1]

	switch command {
	case "decode":
		bencodedValue := os.Args[2]

		decoded, err := bencode.DecodeBencode(bencodedValue)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		jsonOutput, _ := json.Marshal(decoded)
		fmt.Println(string(jsonOutput))
	case "info":
		filePath := os.Args[2]

		torrent, err := NewTorrentFile(filePath, 1234)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		fmt.Printf("Tracker URL: %s\n", torrent.Announce)
		fmt.Printf("Length: %d\n", torrent.Info.Length)
		infoHash, err := torrent.InfoHash()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		} else {
			fmt.Printf("Info Hash: %x\n", infoHash)
		}
		fmt.Printf("Piece Length: %d\n", torrent.Info.PieceLength)
		fmt.Printf("Piece Hashes:\n")
		for i := 0; i < len(torrent.Info.Pieces); i += 20 {
			fmt.Printf("%x\n", torrent.Info.Pieces[i:i+20])
		}
		return
	case "peers":
		filePath := os.Args[2]

		torrent, err := NewTorrentFile(filePath, 1234)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		response, err := torrent.GetTrackerResponse()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		for i := 0; i < len(response.Peers); i++ {
			fmt.Println(response.Peers[i])
		}
	case "handshake":
		filePath := os.Args[2]
		peerInfo := os.Args[3]

		torrent, err := NewTorrentFile(filePath, 1234)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		msg := PeerWireMessage{
			buffer: make([]byte, 1024),
		}

		conn, err := net.Dial("tcp", peerInfo)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		defer conn.Close()

		rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

		// Handshake
		if err = torrent.FillHandshakeMessage(&msg); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		if err = HandlePeerWireProtocol(rw, &msg); err != nil {
			fmt.Printf("failed to send handshake: %s\n", err)
			os.Exit(1)
		}

		fmt.Printf("Peer ID: %x\n", msg.buffer[OffsetHandshakePeerId:LenHandshakeMsg])

	case "download_piece":
		// ./your_bittorrent.sh download_piece -o ./test-piece-0 sample.torrent 0
		outputPath := os.Args[3]
		filePath := os.Args[4]
		pieceIndex, err := strconv.Atoi(os.Args[5])
		if err != nil {
			fmt.Printf("failed to parse pieceIndex: %s\n", err)
			os.Exit(1)
		}

		torrent, err := NewTorrentFile(filePath, 1234)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		trackerResponse, err := torrent.GetTrackerResponse()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		// FIXME: check if this actually sets the capacity to hold whole piece
		pieceBuffer := bytes.NewBuffer(make([]byte, 0, torrent.Info.PieceLength))

		// make sure that it has enough space in case different messages are received
		msg := PeerWireMessage{
			buffer: make([]byte, LenRequestBlockLength*2),
		}

		// Connection to a peer
		peerIndex := 0
		conn, err := net.Dial("tcp", trackerResponse.Peers[peerIndex].String())
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		defer conn.Close()

		rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

		// Handshake
		if err = torrent.FillHandshakeMessage(&msg); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		if err = HandlePeerWireProtocol(rw, &msg); err != nil {
			fmt.Printf("failed handshake: %s\n", err)
			os.Exit(1)
		}

		// no more handshake messages from here on
		msg.handshake = false

		fmt.Printf("Peer ID: %x\n", msg.buffer[OffsetHandshakePeerId:LenHandshakeMsg])

		// Step 1: wait for bitfield message from the peer
		// The bitfield message may only be sent immediately after the handshaking sequence is completed,
		// and BEFORE any other messages are sent.
		// It is optional, and need not be sent if a client has no pieces.
		receivedBitfield := false
		if msg.len > LenHandshakeMsg {
			for offset := LenHandshakeMsg; offset < msg.len; {
				lenPrefix := msg.toInt(offset)
				fmt.Printf("LengthPrefix=%d\n", lenPrefix)
				switch {
				case lenPrefix == 0:
				//	fmt.Println("received \"keep-alive\" peer message after handshake")
				default:
					//fmt.Printf("received \"%s\" peer message after handshake\n", msg.toMsgId(offset+4))
					// TODO: implement bitfield checks
					receivedBitfield = true
				}

				offset += 4 + lenPrefix
			}
		}

		for !receivedBitfield {
			// just send a keep-alive until we receive bitfield
			clear(msg.buffer[0:4])
			msg.len = 4
			if err = HandlePeerWireProtocol(rw, &msg); err != nil {
				fmt.Printf("failed receiving \"bitfield\" peer message: %s\n", err)
				os.Exit(1)
			}

			for offset := 0; offset < msg.len; {
				lenPrefix := msg.toInt(offset)

				switch {
				case lenPrefix == 0:
					//fmt.Println("received \"keep-alive\" peer message")
				default:
					//fmt.Printf("received \"%s\" peer message\n", msg.toMsgId(offset+4))
					// TODO: implement bitfield checks
					receivedBitfield = true
				}

				offset += 4 + lenPrefix
			}
		}

		// Step 2: send an interested message
		// Step 3: wait for unchoke message
		for receivedUnchoke := false; !receivedUnchoke; {
			copy(msg.buffer, []byte{0, 0, 0, 1, byte(Interested)})
			msg.len = 5
			if err = HandlePeerWireProtocol(rw, &msg); err != nil {
				fmt.Printf("failed receiving \"unchoke\" peer message: %s\n", err)
				os.Exit(1)
			}
			for offset := 0; offset < msg.len; {
				lenPrefix := msg.toInt(offset)
				switch {
				case lenPrefix == 0:
					//fmt.Println("received \"keep-alive\" peer message")
				default:
					//fmt.Printf("received \"%s\" peer message\n", msg.toMsgId(offset+4))
					if msg.toMsgId(offset+4) == UnChoke {
						receivedUnchoke = true
					}
				}

				offset += 4 + lenPrefix
			}
		}

		// Step 4: send a request messages until a piece is downloaded
		pieceLength := torrent.Info.PieceLength
		piecesTotal := torrent.Info.Length / pieceLength
		if torrent.Info.Length%pieceLength != 0 {
			piecesTotal += 1
			if pieceIndex+1 == piecesTotal {
				pieceLength = torrent.Info.Length % pieceLength
			}
		}

		fmt.Printf("PieceIndex=%d totalPieces=%d pieceLength=%d\n", pieceIndex, piecesTotal, pieceLength)

		for pieceBuffer.Len() < pieceLength {
			blockLength := LenRequestBlockLength
			if pieceBuffer.Len()+blockLength > pieceLength {
				blockLength = pieceLength - pieceBuffer.Len()
			}

			// FIXME: error handling
			_ = torrent.FillRequestMessage(&msg, pieceIndex, pieceBuffer.Len(), blockLength)

			if err = HandlePeerWireProtocol(rw, &msg); err != nil {
				fmt.Printf("failed receiving \"%s\" peer message, recv=%d: %s\n", Request, pieceBuffer.Len(), err)
				os.Exit(1)
			}

			// handle received messages
			for offset := 0; offset < msg.len; {
				lenPrefix := msg.toInt(offset)
				switch {
				case lenPrefix == 0:
					//fmt.Println("received \"keep-alive\" peer message")
				default:
					msgId := msg.toMsgId(offset + OffsetMsgId)
					//fmt.Printf("received \"%s\" peer message\n", msgId)

					if msgId == Piece {
						//fmt.Printf("pieceBufferLen=%d , adding=%d\n", pieceBuffer.Len(), LenMsgLenPrefix+lenPrefix-OffsetMsgPieceBlock)
						pieceBuffer.Write(msg.buffer[offset+OffsetMsgPieceBlock : offset+LenMsgLenPrefix+lenPrefix])
					}
				}

				offset += LenMsgLenPrefix + lenPrefix
			}
		}

		fmt.Printf("Downloaded piece: length=%d, expected=%d\n", pieceBuffer.Len(), torrent.Info.PieceLength)

		// Step 5: check piece hash
		receivedHash := sha1.Sum(pieceBuffer.Bytes())

		var expectedHash [sha1.Size]byte
		copy(expectedHash[:], torrent.Info.Pieces[pieceIndex*20:pieceIndex*20+20])
		if receivedHash != expectedHash {
			fmt.Printf("expected piece hash != received piece hash; pieceIndex=%d!\n", pieceIndex)
			fmt.Printf("received piece hash: %x\n", receivedHash)
			fmt.Printf("expected piece hash: %x\n", expectedHash)
			fmt.Printf("full     piece hash: %x\n", torrent.Info.Pieces)
			os.Exit(1)
		}

		output, err := os.Create(outputPath)
		if err != nil {
			fmt.Println("Failed to create output file:", err)
			os.Exit(1)
		}
		outputWriter := bufio.NewWriter(output)
		if _, err = outputWriter.Write(pieceBuffer.Bytes()); err != nil {
			fmt.Println("Failed to create output file:", err)
			os.Exit(1)
		}
		if err = outputWriter.Flush(); err != nil {
			fmt.Println("Failed to flush to output file:", err)
			os.Exit(1)
		}

	case "download":
		// ./your_bittorrent.sh download -o /tmp/test.txt sample.torrent
		outputPath := os.Args[3]
		filePath := os.Args[4]

		torrent, err := NewTorrentFile(filePath, 1234)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		trackerResponse, err := torrent.GetTrackerResponse()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		// FIXME: check if this actually sets the capacity to hold whole piece
		pieceBuffer := bytes.NewBuffer(make([]byte, 0, torrent.Info.PieceLength))

		// make sure that it has enough space in case different messages are received
		msg := PeerWireMessage{
			buffer: make([]byte, LenRequestBlockLength*2),
		}

		// Connection to a peer
		peerIndex := 0
		conn, err := net.Dial("tcp", trackerResponse.Peers[peerIndex].String())
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		defer conn.Close()

		rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

		// Download pieces
		piecesTotal := torrent.Info.Length / torrent.Info.PieceLength
		if torrent.Info.Length%torrent.Info.PieceLength != 0 {
			piecesTotal += 1
		}
		for i := 0; i < piecesTotal; i++ {
			pieceBuffer.Reset()
			if err = downloadPiece(torrent, rw, &msg, pieceBuffer, i, outputPath+strconv.Itoa(i)); err != nil {
				fmt.Printf("Failed to download pieceIndex=%d: %s\n", i, err)
				os.Exit(1)
			}
		}

		outputFile, err := os.Create(outputPath)
		if err != nil {
			fmt.Printf("Failed to create output file: %s\n", err)
			os.Exit(1)
		}
		defer outputFile.Close()
		outputWriter := bufio.NewWriter(outputFile)

		for i := 0; i < piecesTotal; i++ {
			inputPath := outputPath + strconv.Itoa(i)
			inputPiece, err := os.Open(inputPath)
			if err != nil {
				fmt.Printf("Failed to open piece file %s: %s\n", inputPath, err)
				os.Exit(1)
			}
			defer inputPiece.Close()

			inputReader := bufio.NewReader(inputPiece)

			_, err = outputWriter.ReadFrom(inputReader)
			if err != nil {
				fmt.Printf("Failed to read from %s to %s: %s\n", inputPath, outputPath)
				os.Exit(1)
			}

			// FIXME: is it ok to delete the piece? What if consequent piece fails?
			if err = os.Remove(inputPath); err != nil {
				fmt.Printf("Failed to remove %s: %s\n", inputPath, err)
				os.Exit(1)
			}
		}
		if err = outputWriter.Flush(); err != nil {
			fmt.Printf("Failed to flush to output file %s: %s\n", outputPath, err)
			os.Exit(1)
		}

		fmt.Printf("Downloaded file: %s\n", outputPath)

	default:
		fmt.Println("Unknown command: " + command)
	}
}

func downloadPiece(torrent *TorrentFile, rw *bufio.ReadWriter, msg *PeerWireMessage, pieceBuffer *bytes.Buffer, pieceIndex int, outputPath string) error {
	var err error

	// FIXME: on errors or retries, this might not be the case!
	if pieceIndex == 0 {
		// Handshake
		if err = torrent.FillHandshakeMessage(msg); err != nil {
			return err
		}

		if err = HandlePeerWireProtocol(rw, msg); err != nil {
			return fmt.Errorf("failed handshake: %s\n", err)
		}

		// no more handshake messages from here on
		msg.handshake = false

		fmt.Printf("Peer ID: %x\n", msg.buffer[OffsetHandshakePeerId:LenHandshakeMsg])

		// Step 1: wait for bitfield message from the peer
		// The bitfield message may only be sent immediately after the handshaking sequence is completed,
		// and BEFORE any other messages are sent.
		// It is optional, and need not be sent if a client has no pieces.
		receivedBitfield := false
		if msg.len > LenHandshakeMsg {
			for offset := LenHandshakeMsg; offset < msg.len; {
				lenPrefix := msg.toInt(offset)
				fmt.Printf("LengthPrefix=%d\n", lenPrefix)
				switch {
				case lenPrefix == 0:
				//	fmt.Println("received \"keep-alive\" peer message after handshake")
				default:
					//fmt.Printf("received \"%s\" peer message after handshake\n", msg.toMsgId(offset+4))
					// TODO: implement bitfield checks
					receivedBitfield = true
				}

				offset += 4 + lenPrefix
			}
		}

		for !receivedBitfield {
			// just send a keep-alive until we receive bitfield
			clear(msg.buffer[0:4])
			msg.len = 4
			if err = HandlePeerWireProtocol(rw, msg); err != nil {
				return fmt.Errorf("failed receiving \"bitfield\" peer message: %s\n", err)
			}

			for offset := 0; offset < msg.len; {
				lenPrefix := msg.toInt(offset)

				switch {
				case lenPrefix == 0:
					//fmt.Println("received \"keep-alive\" peer message")
				default:
					//fmt.Printf("received \"%s\" peer message\n", msg.toMsgId(offset+4))
					// TODO: implement bitfield checks
					receivedBitfield = true
				}

				offset += 4 + lenPrefix
			}
		}

		// Step 2: send an interested message
		// Step 3: wait for unchoke message
		for receivedUnchoke := false; !receivedUnchoke; {
			copy(msg.buffer, []byte{0, 0, 0, 1, byte(Interested)})
			msg.len = 5
			if err = HandlePeerWireProtocol(rw, msg); err != nil {
				return fmt.Errorf("failed receiving \"unchoke\" peer message: %s\n", err)
			}
			for offset := 0; offset < msg.len; {
				lenPrefix := msg.toInt(offset)
				switch {
				case lenPrefix == 0:
					//fmt.Println("received \"keep-alive\" peer message")
				default:
					//fmt.Printf("received \"%s\" peer message\n", msg.toMsgId(offset+4))
					if msg.toMsgId(offset+4) == UnChoke {
						receivedUnchoke = true
					}
				}

				offset += 4 + lenPrefix
			}
		}
	}

	// Step 4: send a request messages until a piece is downloaded
	pieceLength := torrent.Info.PieceLength
	piecesTotal := torrent.Info.Length / pieceLength
	if torrent.Info.Length%pieceLength != 0 {
		piecesTotal += 1
		if pieceIndex+1 == piecesTotal {
			pieceLength = torrent.Info.Length % pieceLength
		}
	}

	fmt.Printf("PieceIndex=%d totalPieces=%d pieceLength=%d\n", pieceIndex, piecesTotal, pieceLength)

	for pieceBuffer.Len() < pieceLength {
		blockLength := LenRequestBlockLength
		if pieceBuffer.Len()+blockLength > pieceLength {
			blockLength = pieceLength - pieceBuffer.Len()
		}

		// FIXME: error handling
		_ = torrent.FillRequestMessage(msg, pieceIndex, pieceBuffer.Len(), blockLength)

		if err = HandlePeerWireProtocol(rw, msg); err != nil {
			return fmt.Errorf("failed receiving \"%s\" peer message, recv=%d: %s\n", Request, pieceBuffer.Len(), err)
		}

		// handle received messages
		for offset := 0; offset < msg.len; {
			lenPrefix := msg.toInt(offset)
			switch {
			case lenPrefix == 0:
				//fmt.Println("received \"keep-alive\" peer message")
			default:
				msgId := msg.toMsgId(offset + OffsetMsgId)
				//fmt.Printf("received \"%s\" peer message\n", msgId)

				if msgId == Piece {
					//fmt.Printf("pieceBufferLen=%d , adding=%d\n", pieceBuffer.Len(), LenMsgLenPrefix+lenPrefix-OffsetMsgPieceBlock)
					pieceBuffer.Write(msg.buffer[offset+OffsetMsgPieceBlock : offset+LenMsgLenPrefix+lenPrefix])
				}
			}

			offset += LenMsgLenPrefix + lenPrefix
		}
	}

	fmt.Printf("Downloaded pieceIndex=%d: length=%d, expected=%d\n", pieceIndex, pieceBuffer.Len(), pieceLength)

	// Step 5: check piece hash
	receivedHash := sha1.Sum(pieceBuffer.Bytes())

	var expectedHash [sha1.Size]byte
	copy(expectedHash[:], torrent.Info.Pieces[pieceIndex*20:pieceIndex*20+20])
	if receivedHash != expectedHash {
		fmt.Printf("expected piece hash != received piece hash; pieceIndex=%d!\n", pieceIndex)
		fmt.Printf("received piece hash: %x\n", receivedHash)
		fmt.Printf("expected piece hash: %x\n", expectedHash)
		fmt.Printf("full     piece hash: %x\n", torrent.Info.Pieces)
		return fmt.Errorf("expected piece hash != received piece hash; pieceIndex=%d!\n", pieceIndex)
	}

	output, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %s\n", err)
	}
	defer output.Close()

	outputWriter := bufio.NewWriter(output)
	if _, err = outputWriter.Write(pieceBuffer.Bytes()); err != nil {
		return fmt.Errorf("failed to create output file: %s\n", err)
	}
	if err = outputWriter.Flush(); err != nil {
		return fmt.Errorf("failed to flush to output file: %s\n", err)
	}

	return nil
}

// There are following things:
// 1. Read tracker URL from torrent file
// 2. Perform tracker GET request
// 3. Establish TCP connection with a peer
// 3.1 Perform the handshake
// 3.2 Exchange messages to download the file
// 3.2.1 Multiple peer messages are required to download a piece, which might also fail

// Steps 1. and 2. should be done from single instance - :
//   * Communicate with tracker
//   * Keep track of downloaded pieces
//   * Save pieces to files / save full file

// Step 3. should be done per peer
//   * Handle TCP connection with the peer
//   * Keep track of blocks received
//   * On connection error, report the progress to main instance
//   * When full piece is downloaded, notify main instance and wait for further messages

// This structure is used for PeerWireProtocol, both for sending and receiving messages
// it contains a buffer that contains the message that should be sent and then is used
// for receiving data. If handshake bool is set, then it is a handshake, otherwise
// it will wait to receive all bytes in the length prefix
// if len is <0 , then no message will be sent, only waited for
type PeerWireMessage struct {
	buffer    []byte
	len       int
	handshake bool
}

// decode bigEndian uint32 from buffer index
func (msg *PeerWireMessage) toInt(index int) int {
	return int(binary.BigEndian.Uint32(msg.buffer[index:]))
}

// convert byte to PeerWireMessageId at buffer index
func (msg *PeerWireMessage) toMsgId(index int) PeerWireMessageId {
	return PeerWireMessageId(msg.buffer[index])
}

// HandlePeerWireProtocol
// Note: provided message must ensure that there is enough space in the buffer
// Note: following handshake response, there might be peer messages in the same buffer
// TODO: check for keep-alive messages, so that they dont overflow the buffer
func HandlePeerWireProtocol(rw *bufio.ReadWriter, msg *PeerWireMessage) error {
	if msg.len > 0 {
		_, err := rw.Write(msg.buffer[:msg.len])
		if err != nil {
			return errors.New("failed to write message to TCP writer: " + err.Error())
		}
		if err = rw.Flush(); err != nil {
			return errors.New("failed to flush TCP writer: " + err.Error())
		}
	}

	var err error
	msg.len, err = rw.Read(msg.buffer)
	if err != nil {
		if err == io.EOF {
			return err
		}
		return errors.New("failed to read message from TCP reader: " + err.Error())
	}
	if msg.len < 4 {
		// this applies also to handshake messages
		return errors.New("received peer message is too short")
	}
	if msg.handshake {
		// handshake messages do not have length-prefix
		return nil
	}

	lengthPrefix := int(binary.BigEndian.Uint32(msg.buffer))
	for msg.len < lengthPrefix+4 {
		received, err := rw.Read(msg.buffer[msg.len:])
		if err != nil {
			if err == io.EOF {
				return err
			} else {
				return errors.New("failed to read message from TCP reader: " + err.Error())
			}
		}

		msg.len += received
	}

	if msg.len > lengthPrefix+4 {
		return fmt.Errorf("received peer message is too long: %d", msg.len)
	}

	return nil
}

type PeerWireMessageId byte

const (
	Choke PeerWireMessageId = iota
	UnChoke
	Interested
	NotInterested
	Have
	Bitfield
	Request
	Piece
	Cancel
)

var PeerWireMessageIds = map[PeerWireMessageId]string{
	Choke:         "choke",
	UnChoke:       "unchoke",
	Interested:    "interested",
	NotInterested: "not interested",
	Have:          "have",
	Bitfield:      "bitfield",
	Request:       "request",
	Piece:         "piece",
	Cancel:        "cancel",
}

func (id PeerWireMessageId) String() string {
	return PeerWireMessageIds[id]
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

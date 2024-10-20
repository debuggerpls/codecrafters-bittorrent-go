package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"github.com/codecrafters-io/bittorrent-starter-go/pkg/bittorrent"
	"log"
	"net"
	"os"
	"strconv"
)

// Ensures gofmt doesn't remove the "os" encoding/json import (feel free to remove this!)
var _ = json.Marshal

func main() {
	command := os.Args[1]

	log.SetFlags(log.Lmicroseconds)

	switch command {
	case "decode":
		bencodedValue := os.Args[2]

		decoded, err := bittorrent.DecodeBencode(bencodedValue)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		jsonOutput, _ := json.Marshal(decoded)
		fmt.Println(string(jsonOutput))
	case "info":
		filePath := os.Args[2]

		torrent, err := bittorrent.NewTorrentFile(filePath, 1234)
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

		torrent, err := bittorrent.NewTorrentFile(filePath, 1234)
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

		torrent, err := bittorrent.NewTorrentFile(filePath, 1234)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		conn, err := net.Dial("tcp", peerInfo)

		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		defer conn.Close()

		incoming := make(chan bittorrent.Message)
		errs := make(chan error)
		go bittorrent.HandleIncomingMessages(conn, incoming, errs)

		infoHash, err := torrent.InfoHash()
		if err != nil {
			fmt.Println("fail infohash:", err)
			os.Exit(1)
		}

		handshake := bittorrent.NewHandshakeMessage(torrent.Progress.PeerID, infoHash)
		_, err = handshake.WriteTo(conn)
		if err != nil {
			fmt.Println("fail to send handshake:", err)
			os.Exit(1)
		}

		select {
		case in := <-incoming:
			if in.Type() != bittorrent.HANDSHAKE {
				fmt.Printf("expected handshake, but got %s\n", in.Type())
				os.Exit(1)
			}
			fmt.Printf("Peer ID: %x\n", in.AsHandshake().PeerId())
			return
		case err := <-errs:
			fmt.Printf("received error: %s", err)
			os.Exit(1)
		}

	case "download_piece":
		// ./your_bittorrent.sh download_piece -o ./test-piece-0 sample.torrent 0
		outputPath := os.Args[3]
		filePath := os.Args[4]
		pieceIndex, err := strconv.Atoi(os.Args[5])
		if err != nil {
			fmt.Printf("failed to parse pieceIndex: %s\n", err)
			os.Exit(1)
		}

		torrent, err := bittorrent.NewTorrentFile(filePath, 1234)
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
		msg := bittorrent.PeerWireMessage{
			Buffer: make([]byte, bittorrent.LenRequestBlockLength*2),
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

		if err = bittorrent.HandlePeerWireProtocol(rw, &msg); err != nil {
			fmt.Printf("failed handshake: %s\n", err)
			os.Exit(1)
		}

		// no more handshake messages from here on
		msg.Handshake = false

		fmt.Printf("Peer ID: %x\n", msg.Buffer[bittorrent.OffsetHandshakePeerId:bittorrent.LenHandshakeMsg])

		// Step 1: wait for bitfield message from the peer
		// The bitfield message may only be sent immediately after the handshaking sequence is completed,
		// and BEFORE any other messages are sent.
		// It is optional, and need not be sent if a client has no pieces.
		receivedBitfield := false
		if msg.Len > bittorrent.LenHandshakeMsg {
			for offset := bittorrent.LenHandshakeMsg; offset < msg.Len; {
				lenPrefix := msg.ToInt(offset)
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
			clear(msg.Buffer[0:4])
			msg.Len = 4
			if err = bittorrent.HandlePeerWireProtocol(rw, &msg); err != nil {
				fmt.Printf("failed receiving \"bitfield\" peer message: %s\n", err)
				os.Exit(1)
			}

			for offset := 0; offset < msg.Len; {
				lenPrefix := msg.ToInt(offset)

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
			copy(msg.Buffer, []byte{0, 0, 0, 1, byte(bittorrent.INTERESTED)})
			msg.Len = 5
			if err = bittorrent.HandlePeerWireProtocol(rw, &msg); err != nil {
				fmt.Printf("failed receiving \"unchoke\" peer message: %s\n", err)
				os.Exit(1)
			}
			for offset := 0; offset < msg.Len; {
				lenPrefix := msg.ToInt(offset)
				switch {
				case lenPrefix == 0:
					//fmt.Println("received \"keep-alive\" peer message")
				default:
					//fmt.Printf("received \"%s\" peer message\n", msg.toMsgId(offset+4))
					if msg.ToMsgId(offset+4) == bittorrent.UNCHOKE {
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
			blockLength := bittorrent.LenRequestBlockLength
			if pieceBuffer.Len()+blockLength > pieceLength {
				blockLength = pieceLength - pieceBuffer.Len()
			}

			// FIXME: error handling
			_ = torrent.FillRequestMessage(&msg, pieceIndex, pieceBuffer.Len(), blockLength)

			if err = bittorrent.HandlePeerWireProtocol(rw, &msg); err != nil {
				fmt.Printf("failed receiving \"%s\" peer message, recv=%d: %s\n", bittorrent.REQUEST, pieceBuffer.Len(), err)
				os.Exit(1)
			}

			// handle received messages
			for offset := 0; offset < msg.Len; {
				lenPrefix := msg.ToInt(offset)
				switch {
				case lenPrefix == 0:
					//fmt.Println("received \"keep-alive\" peer message")
				default:
					msgId := msg.ToMsgId(offset + bittorrent.OffsetMsgId)
					//fmt.Printf("received \"%s\" peer message\n", msgId)

					if msgId == bittorrent.PIECE {
						//fmt.Printf("pieceBufferLen=%d , adding=%d\n", pieceBuffer.Len(), LenMsgLenPrefix+lenPrefix-OffsetMsgPieceBlock)
						pieceBuffer.Write(msg.Buffer[offset+bittorrent.OffsetMsgPieceBlock : offset+bittorrent.LenMsgLenPrefix+lenPrefix])
					}
				}

				offset += bittorrent.LenMsgLenPrefix + lenPrefix
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

		torrent, err := bittorrent.NewTorrentFile(filePath, 1234)
		if err != nil {
			log.Println(err)
			os.Exit(1)
		}
		log.Printf("Torrent file size: %d", torrent.Info.Length)

		trackerResponse, err := torrent.GetTrackerResponse()
		if err != nil {
			log.Println(err)
			os.Exit(1)
		}

		infoHash, err := torrent.InfoHash()
		if err != nil {
			log.Println(err)
			return
		}

		totalPieces := len(torrent.Info.Pieces) / 20
		pieces := make([]*bittorrent.Piece, totalPieces)
		pieceLength := torrent.Info.PieceLength
		for i := 0; i < totalPieces; i++ {
			pieces[i] = &bittorrent.Piece{
				Idx:  i,
				Len:  pieceLength,
				Path: outputPath + strconv.Itoa(i),
				// FIXME: might need to copy these
				InfoHash: infoHash,
				PeerId:   torrent.Progress.PeerID,
			}
			copy(pieces[i].Hash[:], torrent.Info.Pieces[i*20:i*20+20])

		}
		if torrentLen := torrent.Info.Length; torrentLen%pieceLength != 0 {
			lastPiece := pieces[totalPieces-1]
			lastPiece.Len = torrentLen % pieceLength
		}

		todo := make(chan *bittorrent.Piece, totalPieces)
		done := make(chan *bittorrent.Piece, totalPieces)
		errs := make(chan error, totalPieces)
		for i := 0; i < totalPieces; i++ {
			todo <- pieces[i]
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// FIXME: connect only to peers == totalPieces
		for _, peer := range trackerResponse.Peers {
			go bittorrent.PeerWorker(ctx, peer.String(), torrent, todo, done, errs)
		}

		for doneCnt := 0; doneCnt < totalPieces; {
			select {
			case err := <-errs:
				// TODO: how to check if there are no more active PeerWorkers -> exit the program!
				log.Println("Failed PeerWorker:", err)

			case piece := <-done:
				if piece.Done {
					doneCnt++
					log.Printf("piece done: idx=%v\n", piece.Idx)
				} else {
					// retry downloading the piece
					log.Printf("piece failed, retry: idx=%v\n", piece.Idx)
					piece.Buffer.Reset()
					todo <- piece
				}
			}
		}

		outputFile, err := os.Create(outputPath)
		if err != nil {
			fmt.Printf("Failed to create output file: %s\n", err)
			os.Exit(1)
		}
		defer outputFile.Close()
		outputWriter := bufio.NewWriter(outputFile)

		// TODO: if we hold the buffers in the pieces, do we need to save pieces before?
		for i := 0; i < totalPieces; i++ {
			inputPath := pieces[i].Path
			inputPiece, err := os.Open(inputPath)
			if err != nil {
				fmt.Printf("Failed to open piece file %s: %s\n", inputPath, err)
				os.Exit(1)
			}
			defer inputPiece.Close()

			inputReader := bufio.NewReader(inputPiece)

			_, err = outputWriter.ReadFrom(inputReader)
			if err != nil {
				fmt.Printf("Failed to read from %s to %s: %s\n", inputPath, outputPath, err)
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

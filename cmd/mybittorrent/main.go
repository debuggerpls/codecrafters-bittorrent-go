package main

import (
	"bufio"
	"context"
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

		infoHash, err := torrent.InfoHash()
		if err != nil {
			log.Println(err)
			return
		}

		totalPieces := len(torrent.Info.Pieces) / 20
		pieceLength := torrent.Info.PieceLength
		piece := &bittorrent.Piece{
			Idx:  pieceIndex,
			Len:  pieceLength,
			Path: outputPath,
			// FIXME: might need to copy these
			InfoHash: infoHash,
			PeerId:   torrent.Progress.PeerID,
		}
		copy(piece.Hash[:], torrent.Info.Pieces[pieceIndex*20:pieceIndex*20+20])

		if torrentLen := torrent.Info.Length; torrentLen%pieceLength != 0 {
			if pieceIndex == totalPieces-1 {
				piece.Len = torrentLen % pieceLength
			}
		}

		todo := make(chan *bittorrent.Piece, 1)
		done := make(chan *bittorrent.Piece)
		errs := make(chan error)
		todo <- piece

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go bittorrent.PeerWorker(ctx, trackerResponse.Peers[0].String(), torrent, todo, done, errs)

		select {
		case err := <-errs:
			// TODO: how to check if there are no more active PeerWorkers -> exit the program!
			log.Println("failed PeerWorker:", err)
			os.Exit(1)

		case piece := <-done:
			if piece.Done {
				log.Printf("piece download done: idx=%v path=%s\n", piece.Idx, piece.Path)
				return
			} else {
				// retry downloading the piece
				log.Printf("piece download failed: idx=%v\n", piece.Idx)
			}
		}
		os.Exit(1)

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
	case "magnet_parse":
		magnetURL := os.Args[2]

		magnetLink, err := bittorrent.NewMagnetLink(magnetURL, 1234)
		bittorrent.AssertNotNil(err, "parse error: %s\n", err)

		fmt.Printf("Tracker URL: %s\n", magnetLink.TrackerUrl())
		fmt.Printf("Info Hash: %s\n", magnetLink.InfoHashString())
	case "magnet_handshake":
		magnetURL := os.Args[2]

		magnetLink, err := bittorrent.NewMagnetLink(magnetURL, 1234)
		bittorrent.AssertNotNil(err, "parse error: %s\n", err)

		response, err := magnetLink.GetTrackerResponse()
		bittorrent.AssertNotNil(err, "failed to get tracker response: %s", err)

		peerInfo := response.Peers[0].String()
		conn, err := net.Dial("tcp", peerInfo)

		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		defer conn.Close()

		incoming := make(chan bittorrent.Message)
		errs := make(chan error)
		go bittorrent.HandleIncomingMessages(conn, incoming, errs)

		infoHash, err := magnetLink.InfoHash()
		if err != nil {
			fmt.Println("fail infohash:", err)
			os.Exit(1)
		}

		handshake := bittorrent.NewHandshakeMessage(magnetLink.PeerId, infoHash)
		//fmt.Printf("Handshake: %x \n", handshake.Data)
		handshake.AsHandshake().SetExtensions()
		//fmt.Printf("Handshake: %x \n", handshake.Data)
		_, err = handshake.WriteTo(conn)
		if err != nil {
			fmt.Println("fail to send handshake:", err)
			os.Exit(1)
		}

		state := struct {
			doneHandshake bool
			doneBitfield  bool
			peerExtended  bool
			peerId        [20]byte
		}{}
		_ = state

		for {
			select {
			case in := <-incoming:
				//fmt.Printf("Received: %s\n", in.Type())
				switch in.Type() {
				case bittorrent.HANDSHAKE:
					if state.doneHandshake {
						panic("Handshake is done already")
					}
					state.doneHandshake = true
					peerId := in.AsHandshake().PeerId()
					state.peerExtended = in.AsHandshake().HasExtensions()
					copy(state.peerId[:], peerId[:])
					//if state.peerExtended {
					//	fmt.Printf("Peer has Extension bit set\n")
					//}
				case bittorrent.BITFIELD:
					if state.doneBitfield {
						panic("Bitfield already received")
					}
					state.doneBitfield = true
					//send the extension handshake
					if state.peerExtended {
						extended := bittorrent.NewExtendedMessage()
						//fmt.Printf("Sending l=%d: %x \n", extended.Len, extended.Data)

						d := map[string]interface{}{
							"m": map[string]interface{}{
								"ut_metadata": 1,
								"ut_pex":      2,
							},
						}
						msg := extended.AsExtended().AddDict(d)
						//fmt.Printf("Sending l=%d: %x \n", msg.Len, msg.Data)
						_, err = msg.WriteTo(conn)
						if err != nil {
							fmt.Println("fail to send extended:", err)
							os.Exit(1)
						}
					}

				case bittorrent.EXTENDED:
					decoded, err := bittorrent.DecodeBencode(string(in.AsExtended().ExtensionDict()))
					if err != nil {
						panic("Failed to decode dict:" + err.Error())
					}

					//fmt.Printf("Extended:\n%q\n", decoded)
					//if state.peerExtended {
					//	extended := bittorrent.NewExtendedMessage()
					//	d := map[string]interface{}{
					//		"m": map[string]interface{}{
					//			"ut_metadata": 2,
					//		},
					//	}
					//	extended.AsExtended().AddDict(d)
					//	_, err = extended.WriteTo(conn)
					//	if err != nil {
					//		fmt.Println("fail to send extended:", err)
					//		os.Exit(1)
					//	}
					//}
					fmt.Printf("Peer ID: %x\n", state.peerId)
					mDict := decoded.(map[string]interface{})["m"].(map[string]interface{})
					fmt.Printf("Peer Metadata Extension ID: %d\n", mDict["ut_metadata"])
					return
				default:
					panic("unhandled default case")
				}
				//if in.Type() != bittorrent.HANDSHAKE {
				//	fmt.Printf("expected handshake, but got %s\n", in.Type())
				//	os.Exit(1)
				//}
				//fmt.Printf("Peer ID: %x\n", in.AsHandshake().PeerId())
				//return
			case err := <-errs:
				fmt.Printf("received error: %s", err)
				os.Exit(1)
			}
		}

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

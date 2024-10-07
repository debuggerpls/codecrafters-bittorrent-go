package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
	// bencode "github.com/jackpal/bencode-go" // Available if you need it!
)

// Ensures gofmt doesn't remove the "os" encoding/json import (feel free to remove this!)
var _ = json.Marshal

func encodeInteger(i int) string {
	return fmt.Sprintf("i%se", strconv.Itoa(i))
}

func encodeString(s string) string {
	return fmt.Sprintf("%d:%s", len(s), s)
}

// Example:
// - 5:hello -> hello
// - 10:hello12345 -> hello12345
func decodeBencodeString(bencodedString string) (string, int, error) {
	index := 0

	switch {
	case unicode.IsDigit(rune(bencodedString[index])):
		var firstColonIndex int

		for i := 0; i < len(bencodedString); i++ {
			if bencodedString[i] == ':' {
				firstColonIndex = i
				break
			}
		}

		lengthStr := bencodedString[:firstColonIndex]

		length, err := strconv.Atoi(lengthStr)
		if err != nil {
			return "", index, err
		}

		index = firstColonIndex + 1 + length
		decodedString := bencodedString[firstColonIndex+1 : index]

		return decodedString, index, nil
	default:
		return "", 0, fmt.Errorf("invalid BencodeString %q", bencodedString)
	}
}

func decodeBencodeInteger(bencodedString string) (int, int, error) {
	index := 0

	switch c := rune(bencodedString[index]); c {
	case 'i':
		indexEnd := strings.Index(bencodedString, "e")
		integer, err := strconv.Atoi(bencodedString[1:indexEnd])
		if err != nil {
			return 0, 0, err
		}

		index = indexEnd + 1
		return integer, index, nil

	default:
		return 0, 0, fmt.Errorf("invalid BencodeInteger %q", bencodedString)
	}
}

func decodeBencodeList(bencodedString string) ([]interface{}, int, error) {
	index := 0
	depth := 0
	l := make([]interface{}, 0)

	for {
		switch c := rune(bencodedString[index]); {
		case c == 'e':
			return l, index + 1, nil
		case c == 'l':
			if depth == 0 {
				index += 1
				depth += 1
			} else {
				// TODO: refactor to not use recursion
				// this is a nested list
				nl, relIndex, err := decodeBencodeList(bencodedString[index:])
				if err != nil {
					return nil, index, err
				}
				l = append(l, nl)
				index += relIndex
			}
		case c == 'i':
			i, relIndex, err := decodeBencodeInteger(bencodedString[index:])
			if err != nil {
				return nil, index, err
			}
			l = append(l, i)
			index += relIndex
		case unicode.IsDigit(c):
			s, relIndex, err := decodeBencodeString(bencodedString[index:])
			if err != nil {
				return nil, index, err
			}
			l = append(l, s)
			index += relIndex
		default:
			return nil, index, fmt.Errorf("invalid BencodeList %q", bencodedString[index:])
		}
	}
}

func decodeBencodeDict(bencodedString string) (map[string]interface{}, int, error) {
	index := 0
	depth := 0
	isValue := false
	key := ""
	d := make(map[string]interface{})

	for {
		switch c := rune(bencodedString[index]); {
		case c == 'e':
			return d, index + 1, nil
		case c == 'd':
			switch {
			case depth == 0:
				index += 1
				depth += 1
			case isValue:
				// TODO: refactor recursion out
				nd, relIndex, err := decodeBencodeDict(bencodedString[index:])
				if err != nil {
					return nil, index, err
				}
				d[key] = nd
				index += relIndex
				isValue = false
			default:
				return nil, index, fmt.Errorf("invalid BencodeDict %q", bencodedString[index:])
			}
		case unicode.IsDigit(c):
			switch {
			case depth == 0:
				return nil, index, fmt.Errorf("invalid BencodeDict %q", bencodedString[index:])
			case !isValue:
				s, relIndex, err := decodeBencodeString(bencodedString[index:])
				if err != nil {
					return nil, index, err
				}
				key = s
				index += relIndex
				isValue = true
			case isValue:
				s, relIndex, err := decodeBencodeString(bencodedString[index:])
				if err != nil {
					return nil, index, err
				}
				d[key] = s
				index += relIndex
				isValue = false
			default:
				return nil, index, fmt.Errorf("invalid BencodeDict, unexpected string %q", bencodedString[index:])
			}
		case c == 'i':
			if isValue {
				i, relIndex, err := decodeBencodeInteger(bencodedString[index:])
				if err != nil {
					return nil, index, err
				}
				d[key] = i
				index += relIndex
				isValue = false
			} else {
				return nil, index, fmt.Errorf("invalid BencodeDict, unexpected integer %q", bencodedString[index:])
			}
		case c == 'l':
			if isValue {
				l, relIndex, err := decodeBencodeList(bencodedString[index:])
				if err != nil {
					return nil, index, err
				}
				d[key] = l
				index += relIndex
				isValue = false
			} else {
				return nil, index, fmt.Errorf("invalid BencodeDict, unexpected list %q", bencodedString[index:])
			}
		default:
			return nil, index, fmt.Errorf("invalid BencodeDict %q", bencodedString[index:])
		}
	}
}

func decodeBencode(bencodedString string) (interface{}, error) {
	c := rune(bencodedString[0])
	switch {
	case unicode.IsDigit(c):
		result, _, err := decodeBencodeString(bencodedString)
		if err != nil {
			return "", err
		}
		return result, nil
	case c == 'i':
		result, _, err := decodeBencodeInteger(bencodedString)
		if err != nil {
			return "", err
		}
		return result, nil
	case c == 'l':
		result, _, err := decodeBencodeList(bencodedString)
		if err != nil {
			return "", err
		}
		return result, nil
	case c == 'd':
		result, _, err := decodeBencodeDict(bencodedString)
		if err != nil {
			return "", err
		}
		return result, nil
	default:
		return "", fmt.Errorf("Unsupported:\n%s\n", bencodedString)
	}
}

type TorrentFile struct {
	FilePath string
	Announce string
	Info     TorrentFileInfo
}

type TorrentFileInfo struct {
	Length      int
	Name        string
	PieceLength int
	Pieces      string
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

func (torrent *TorrentFile) InfoHash() (string, error) {
	// because we know that torrent file is valid, we can just use the file itself
	data, err := os.ReadFile(torrent.FilePath)
	if err != nil {
		return "", err
	}

	// 4:infod<INFO_CONTENTS>e
	infoStart := bytes.Index(data, []byte("4:info")) + 6
	if infoStart < 0 {
		return "", fmt.Errorf("TorrentFile.info: no info in torrent file")
	}
	return fmt.Sprintf("%x", sha1.Sum(data[infoStart:len(data)-1])), nil
}

func NewTorrentFile(filePath string) (*TorrentFile, error) {
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
		return nil, fmt.Errorf("did not read full torrent file, file size: %d, read: %d", size, len(buf))
	}

	d, _, err := decodeBencodeDict(string(buf))
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

	return torrent, nil
}

func main() {
	command := os.Args[1]

	switch command {
	case "decode":
		bencodedValue := os.Args[2]

		decoded, err := decodeBencode(bencodedValue)
		if err != nil {
			fmt.Println(err)
		}

		jsonOutput, _ := json.Marshal(decoded)
		fmt.Println(string(jsonOutput))
		return
	case "info":
		filePath := os.Args[2]

		torrent, err := NewTorrentFile(filePath)
		if err != nil {
			fmt.Println(err)
		}

		fmt.Printf("Tracker URL: %s\n", torrent.Announce)
		fmt.Printf("Length: %d\n", torrent.Info.Length)
		infoHash, err := torrent.InfoHash()
		if err != nil {
			fmt.Println(err)
		} else {
			fmt.Printf("Info Hash: %s\n", infoHash)
		}
		fmt.Printf("Piece Length: %d\n", torrent.Info.PieceLength)
		fmt.Printf("Piece Hashes:\n")
		for i := 0; i < len(torrent.Info.Pieces); i += 20 {
			fmt.Printf("%x\n", torrent.Info.Pieces[i:i+20])
		}
		return
	default:
		fmt.Println("Unknown command: " + command)
	}

	os.Exit(1)
}

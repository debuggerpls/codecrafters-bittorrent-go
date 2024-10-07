package main

import (
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
			return nil, index, fmt.Errorf("Unknown:\n%s\n", bencodedString[index:])
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
	default:
		return "", fmt.Errorf("Unsupported:\n%s\n", bencodedString)
	}
}

func main() {
	command := os.Args[1]

	if command == "decode" {
		bencodedValue := os.Args[2]

		decoded, err := decodeBencode(bencodedValue)
		if err != nil {
			fmt.Println(err)
			return
		}

		jsonOutput, _ := json.Marshal(decoded)
		fmt.Println(string(jsonOutput))
	} else {
		fmt.Println("Unknown command: " + command)
		os.Exit(1)
	}
}

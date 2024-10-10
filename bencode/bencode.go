package bencode

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

func BencodeInteger(i int) string {
	return fmt.Sprintf("i%se", strconv.Itoa(i))
}

func BencodeString(s string) string {
	return fmt.Sprintf("%d:%s", len(s), s)
}

// Example:
// - 5:hello -> hello
// - 10:hello12345 -> hello12345
func DecodeBencodeString(bencodedString string) (string, int, error) {
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

func DecodeBencodeInteger(bencodedString string) (int, int, error) {
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

func DecodeBencodeList(bencodedString string) ([]interface{}, int, error) {
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
				nl, relIndex, err := DecodeBencodeList(bencodedString[index:])
				if err != nil {
					return nil, index, err
				}
				l = append(l, nl)
				index += relIndex
			}
		case c == 'i':
			i, relIndex, err := DecodeBencodeInteger(bencodedString[index:])
			if err != nil {
				return nil, index, err
			}
			l = append(l, i)
			index += relIndex
		case unicode.IsDigit(c):
			s, relIndex, err := DecodeBencodeString(bencodedString[index:])
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

func DecodeBencodeDict(bencodedString string) (map[string]interface{}, int, error) {
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
				nd, relIndex, err := DecodeBencodeDict(bencodedString[index:])
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
				s, relIndex, err := DecodeBencodeString(bencodedString[index:])
				if err != nil {
					return nil, index, err
				}
				key = s
				index += relIndex
				isValue = true
			case isValue:
				s, relIndex, err := DecodeBencodeString(bencodedString[index:])
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
				i, relIndex, err := DecodeBencodeInteger(bencodedString[index:])
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
				l, relIndex, err := DecodeBencodeList(bencodedString[index:])
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

func DecodeBencode(bencodedString string) (interface{}, error) {
	c := rune(bencodedString[0])
	switch {
	case unicode.IsDigit(c):
		result, _, err := DecodeBencodeString(bencodedString)
		if err != nil {
			return "", err
		}
		return result, nil
	case c == 'i':
		result, _, err := DecodeBencodeInteger(bencodedString)
		if err != nil {
			return "", err
		}
		return result, nil
	case c == 'l':
		result, _, err := DecodeBencodeList(bencodedString)
		if err != nil {
			return "", err
		}
		return result, nil
	case c == 'd':
		result, _, err := DecodeBencodeDict(bencodedString)
		if err != nil {
			return "", err
		}
		return result, nil
	default:
		return "", fmt.Errorf("Unsupported:\n%s\n", bencodedString)
	}
}

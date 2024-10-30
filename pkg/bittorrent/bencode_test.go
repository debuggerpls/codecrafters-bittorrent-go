package bittorrent

import "testing"

func TestEncodeInteger(t *testing.T) {
	tests := []struct {
		input  int
		output string
	}{
		{-1, "i-1e"},
		{0, "i0e"},
		{1, "i1e"},
		{99, "i99e"},
		{1111, "i1111e"},
	}

	for _, v := range tests {
		got := BencodeInteger(v.input)
		want := v.output
		if got != v.output {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

func TestEncodeString(t *testing.T) {
	tests := []struct {
		input  string
		output string
	}{
		{"", "0:"},
		{"hello", "5:hello"},
	}

	for _, v := range tests {
		got := BencodeString(v.input)
		want := v.output
		if got != v.output {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

func TestEncodeList(t *testing.T) {
	tests := []struct {
		input  []interface{}
		output string
	}{
		{make([]interface{}, 0), "le"},
		{[]interface{}{1, 2}, "li1ei2ee"},
		{[]interface{}{1, make([]interface{}, 0)}, "li1elee"},
	}

	for _, v := range tests {
		got := BencodeList(v.input)
		want := v.output
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

func TestEncodeDict(t *testing.T) {
	tests := []struct {
		input  map[string]interface{}
		output string
	}{
		{make(map[string]interface{}), "de"},
		{map[string]interface{}{
			"hello": "world",
		}, "d5:hello5:worlde"},
	}

	for _, v := range tests {
		got := BencodeDict(v.input)
		want := v.output
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}

}

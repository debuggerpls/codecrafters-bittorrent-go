package bittorrent

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type MagnetLink struct {
	magnetURL  *url.URL
	PeerId     [20]byte
	Port       int
	Uploaded   int
	Downloaded int
	Left       int
	Compact    int
}

func NewMagnetLink(rawURL string, port int) (*MagnetLink, error) {
	mUrl, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	if mUrl.Scheme != "magnet" {
		return nil, fmt.Errorf("wrong format, expected=%q", "magnet")
	}

	magnetLink := &MagnetLink{
		magnetURL: mUrl,
		Compact:   1,
		Port:      port,
	}

	_, err = rand.Read(magnetLink.PeerId[:])
	if err != nil {
		return nil, fmt.Errorf("failed to generate peer_id: %s", err)
	}

	return magnetLink, nil
}

func (m *MagnetLink) TrackerUrl() string {
	return m.magnetURL.Query().Get("tr")
}

func (m *MagnetLink) InfoHashString() string {
	urn := "urn:btih:"
	infoHash, found := strings.CutPrefix(m.magnetURL.Query().Get("xt"), urn)
	if !found {
		return ""
	}
	return infoHash
}

func (m *MagnetLink) InfoHash() ([20]byte, error) {
	var hash [20]byte
	_, err := hex.Decode(hash[:], []byte(m.InfoHashString()))
	if err != nil {
		return hash, err
	}
	return hash, nil
}

func (m *MagnetLink) newTrackerRequestURL() (string, error) {
	infoHash, err := m.InfoHash()
	if err != nil {
		return "", err
	}

	trackerParams := url.Values{}
	trackerParams.Set("info_hash", string(infoHash[:]))
	trackerParams.Set("peer_id", string(m.PeerId[:]))
	trackerParams.Set("port", strconv.Itoa(m.Port))
	trackerParams.Set("uploaded", strconv.Itoa(m.Uploaded))
	trackerParams.Set("downloaded", strconv.Itoa(m.Downloaded))
	// TODO: this should be calculated in the future if provided in magnetURL
	//trackerParams.Set("left", strconv.Itoa(m.Left))
	trackerParams.Set("left", strconv.Itoa(1))
	trackerParams.Set("compact", strconv.Itoa(m.Compact))

	trackerRequestURL := fmt.Sprintf("%s?%s", m.TrackerUrl(), trackerParams.Encode())
	return trackerRequestURL, nil
}

func (m *MagnetLink) GetTrackerResponse() (*TrackerResponse, error) {
	trackerRequestURL, err := m.newTrackerRequestURL()
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

	//fmt.Printf("%s\n", string(body))

	// TODO: this whole thing needs to be refactored. Do not expect specific values in the response
	trackerResponse, _, err := DecodeBencodeDict(string(body))
	if err != nil {
		return nil, err
	}

	//fmt.Printf("%v", trackerResponse)

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
		response.Interval = -1
	}

	return response, nil
}

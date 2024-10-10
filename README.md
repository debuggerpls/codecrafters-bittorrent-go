[![progress-banner](https://backend.codecrafters.io/progress/bittorrent/b0ab8177-5cc9-4522-b3d0-64c388050dde)](https://app.codecrafters.io/users/codecrafters-bot?r=2qF)

This is a Go solution to the
["Build Your Own BitTorrent" Challenge](https://app.codecrafters.io/courses/bittorrent/overview).

## BitTorrent Protocol Specification
* https://www.bittorrent.org/beps/bep_0003.html
* https://wiki.theory.org/BitTorrentSpecification

## Tasks

### 1. Decode bencoded strings
```
$ ./your_bittorrent.sh decode 5:hello
"hello"
```

### 2. Decode bencoded integers
```
$ ./your_bittorrent.sh decode i52e
52
```

### 3. Decode bencoded lists
```
$ ./your_bittorrent.sh decode l5:helloi52ee
[“hello”,52]
```

### 4. Decode bencoded dictionaries
```
$ ./your_bittorrent.sh decode l5:helloi52ee
[“hello”,52]
```

### 5. Parse torrent file
```
$ ./your_bittorrent.sh info sample.torrent
Tracker URL: http://bittorrent-test-tracker.codecrafters.io/announce
Length: 92063
```

### 6. Calculate info hash
```
$ ./your_bittorrent.sh info sample.torrent
Tracker URL: http://bittorrent-test-tracker.codecrafters.io/announce
Length: 92063
Info Hash: d69f91e6b2ae4c542468d1073a71d4ea13879a7f
```

### 7. Piece hashes
```
$ ./your_bittorrent.sh info sample.torrent
Tracker URL: http://bittorrent-test-tracker.codecrafters.io/announce
Length: 92063
Info Hash: d69f91e6b2ae4c542468d1073a71d4ea13879a7f
Piece Length: 32768
Piece Hashes:
e876f67a2a8886e8f36b136726c30fa29703022d
6e2275e604a0766656736e81ff10b55204ad8d35
f00d937a0213df1982bc8d097227ad9e909acc17
```

### 8. Discover peers
```
$ ./your_bittorrent.sh peers sample.torrent
165.232.35.114:51533
165.232.38.164:51596
165.232.41.73:51451
```

### 9. Peer handshake
```
$ ./your_bittorrent.sh handshake sample.torrent 165.232.35.114:51533
Peer ID: 2d524e302e302e302dd5c125b7413647829520d9
```

### 9. Download a piece
```
$ ./your_bittorrent.sh download_piece -o ./test-piece-0 sample.torrent 0
$ ls test-piece-0
test-piece-0
```
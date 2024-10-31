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

### 10. Download a single file
```
$ ./your_bittorrent.sh download -o ./tmp/test.txt sample.torrent
$ ls tmp/test.txt
tmp/test.txt
```

### 11. Parse magnet link
```
$ ./your_bittorrent.sh magnet_parse "magnet:?xt=urn:btih:c5fb9894bdaba464811b088d806bdd611ba490af&dn=magnet3.gif&tr=http%3A%2F%2Fbittorrent-test-tracker.codecrafters.io%2Fannounce"
Tracker URL: http://bittorrent-test-tracker.codecrafters.io/announce
Info Hash: c5fb9894bdaba464811b088d806bdd611ba490af

Examples: https://github.com/codecrafters-io/bittorrent-test-seeder/blob/main/torrent_files/magnet_links.txt
```

### 12. Announce extension support
```
$ ./your_bittorrent.sh magnet_handshake "magnet:?xt=urn:btih:c5fb9894bdaba464811b088d806bdd611ba490af&dn=magnet3.gif&tr=http%3A%2F%2Fbittorrent-test-tracker.codecrafters.io%2Fannounce"
Peer ID: 2d524e302e302e302de33db7666c49ec504ffdcb
```

### 13. Send extension handshake
```
$ ./your_bittorrent.sh magnet_handshake "magnet:?xt=urn:btih:c5fb9894bdaba464811b088d806bdd611ba490af&dn=magnet3.gif&tr=http%3A%2F%2Fbittorrent-test-tracker.codecrafters.io%2Fannounce"
Peer ID: 2d524e302e302e302de33db7666c49ec504ffdcb
```

### 14. Receive extension handshake
```
$ ./your_bittorrent.sh magnet_handshake "magnet:?xt=urn:btih:c5fb9894bdaba464811b088d806bdd611ba490af&dn=magnet3.gif&tr=http%3A%2F%2Fbittorrent-test-tracker.codecrafters.io%2Fannounce"
Peer ID: 2d524e302e302e302de33db7666c49ec504ffdcb
```

### 15. Request metadata
```
$ ./your_bittorrent.sh magnet_info "magnet:?xt=urn:btih:c5fb9894bdaba464811b088d806bdd611ba490af&dn=magnet3.gif&tr=http%3A%2F%2Fbittorrent-test-tracker.codecrafters.io%2Fannounce"
```

### 16. Receive metadata
```
$ ./your_bittorrent.sh magnet_info "magnet:?xt=urn:btih:c5fb9894bdaba464811b088d806bdd611ba490af&dn=magnet3.gif&tr=http%3A%2F%2Fbittorrent-test-tracker.codecrafters.io%2Fannounce"
Tracker URL: http://127.0.0.1:43985/announce
Length: 629944
Info Hash: c5fb9894bdaba464811b088d806bdd611ba490af
Piece Length: 262144
Piece Hashes:
ca80fd83ffb34d6e1bbd26a8ef6d305827f1cd0a
707fd7c657f6d636f0583466c3cfe134ddb2c08a
47076d104d214c0052960ef767262649a8af0ea8

Test magnet links:
https://github.com/codecrafters-io/bittorrent-test-seeder/blob/main/torrent_files/magnet_links.txt
```

### 17. Download a piece
```
$ ./your_bittorrent.sh magnet_download_piece -o piece-0 "magnet:?xt=urn:btih:c5fb9894bdaba464811b088d806bdd611ba490af&dn=magnet3.gif&tr=http%3A%2F%2Fbittorrent-test-tracker.codecrafters.io%2Fannounce" 0
```

### 18. Download the whole file
```
$ ./your_bittorrent.sh magnet_download -o sample "magnet:?xt=urn:btih:c5fb9894bdaba464811b088d806bdd611ba490af&dn=magnet3.gif&tr=http%3A%2F%2Fbittorrent-test-tracker.codecrafters.io%2Fannounce"
```

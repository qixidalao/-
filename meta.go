package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/marksamman/bencode"
)

type metaWire struct {
	infohash     string
	from         string
	peerID       string
	conn         *net.TCPConn
	timeout      time.Duration
	metadataSize int
	utMetadata   int
	numOfPieces  int
	pieces       [][]byte
	err          error
}

func newMetaWire(infohash string, from string, timeout time.Duration) *metaWire {
	return &metaWire{
		infohash: infohash,
		from:     from,
		peerID:   string(randBytes(20)),
		timeout:  timeout,
	}
}

func (mw *metaWire) fetch() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mw.timeout)
	defer cancel()
	return mw.fetchCtx(ctx)
}

func (mw *metaWire) fetchCtx(ctx context.Context) ([]byte, error) {
	mw.connect()
	mw.handshake(ctx)
	mw.onHandshake(ctx)
	mw.extHandshake(ctx)

	if mw.err != nil {
		if mw.conn != nil {
			mw.conn.Close()
		}
		return nil, mw.err
	}

	for {
		data, err := mw.next(ctx)
		if err != nil {
			return nil, err
		}

		if len(data) == 0 || data[0] != 20 { // 20 是 extended 消息类型
			continue
		}

		if len(data) < 2 {
			continue
		}
		
		if err := mw.onExtended(ctx, data[1], data[2:]); err != nil {
			return nil, err
		}

		if !mw.checkDone() {
			continue
		}

		m := bytes.Join(mw.pieces, []byte(""))
		sum := sha1.Sum(m)
		if bytes.Equal(sum[:], []byte(mw.infohash)) {
			return m, nil
		}

		return nil, errors.New("metadata checksum mismatch")
	}
}

func (mw *metaWire) connect() {
	conn, err := net.DialTimeout("tcp", mw.from, mw.timeout)
	if err != nil {
		mw.err = fmt.Errorf("connect to remote peer failed: %v", err)
		return
	}

	mw.conn = conn.(*net.TCPConn)
}

func (mw *metaWire) handshake(ctx context.Context) {
	if mw.err != nil {
		return
	}

	select {
	case <-ctx.Done():
		mw.err = context.DeadlineExceeded
		return
	default:
	}

	buf := bytes.NewBuffer(nil)
	buf.Write(mw.preHeader())
	buf.WriteString(mw.infohash)
	buf.WriteString(mw.peerID)
	_, mw.err = mw.conn.Write(buf.Bytes())
}

func (mw *metaWire) onHandshake(ctx context.Context) {
	if mw.err != nil {
		return
	}

	select {
	case <-ctx.Done():
		mw.err = context.DeadlineExceeded
		return
	default:
	}

	res, err := mw.read(ctx, 68)
	if err != nil {
		mw.err = err
		return
	}

	if !bytes.Equal(res[:20], mw.preHeader()[:20]) {
		mw.err = errors.New("remote peer not supporting bittorrent protocol")
		return
	}

	if res[25]&0x10 != 0x10 {
		mw.err = errors.New("remote peer not supporting extension protocol")
		return
	}

	if !bytes.Equal(res[28:48], []byte(mw.infohash)) {
		mw.err = errors.New("invalid bittorrent header response")
		return
	}
}

func (mw *metaWire) extHandshake(ctx context.Context) {
	if mw.err != nil {
		return
	}

	select {
	case <-ctx.Done():
		mw.err = context.DeadlineExceeded
		return
	default:
	}

	data := append([]byte{20, 0}, bencode.Encode(map[string]interface{}{
		"m": map[string]interface{}{
			"ut_metadata": 1,
		},
	})...)
	if err := mw.write(ctx, data); err != nil {
		mw.err = err
		return
	}
}

func (mw *metaWire) onExtHandshake(ctx context.Context, payload []byte) error {
	select {
	case <-ctx.Done():
		return context.DeadlineExceeded
	default:
	}

	dict, err := bencode.Decode(bytes.NewBuffer(payload))
	if err != nil {
		return errors.New("invalid extension header response")
	}

	metadataSize, ok := dict["metadata_size"].(int64)
	if !ok {
		return errors.New("invalid extension header response")
	}

	if metadataSize > 16*1024*1024 { // 16MB
		return errors.New("metadata_size too long")
	}

	if metadataSize < 0 {
		return errors.New("negative metadata_size")
	}

	m, ok := dict["m"].(map[string]interface{})
	if !ok {
		return errors.New("invalid extension header response")
	}

	utMetadata, ok := m["ut_metadata"].(int64)
	if !ok {
		return errors.New("invalid extension header response")
	}

	mw.metadataSize = int(metadataSize)
	mw.utMetadata = int(utMetadata)
	mw.numOfPieces = mw.metadataSize / 16384
	if mw.metadataSize%16384 != 0 {
		mw.numOfPieces++
	}
	mw.pieces = make([][]byte, mw.numOfPieces)

	for i := 0; i < mw.numOfPieces; i++ {
		mw.requestPiece(ctx, i)
	}

	return nil
}

func (mw *metaWire) requestPiece(ctx context.Context, i int) {
	buf := bytes.NewBuffer(nil)
	buf.WriteByte(20)
	buf.WriteByte(byte(mw.utMetadata))
	buf.Write(bencode.Encode(map[string]interface{}{
		"msg_type": 0,
		"piece":    i,
	}))
	mw.write(ctx, buf.Bytes())
}

func (mw *metaWire) onExtended(ctx context.Context, ext byte, payload []byte) error {
	if ext == 0 {
		if err := mw.onExtHandshake(ctx, payload); err != nil {
			return err
		}
	} else {
		piece, index, err := mw.onPiece(ctx, payload)
		if err != nil {
			return err
		}
		mw.pieces[index] = piece
	}
	return nil
}

func (mw *metaWire) onPiece(ctx context.Context, payload []byte) ([]byte, int, error) {
	select {
	case <-ctx.Done():
		return nil, -1, context.DeadlineExceeded
	default:
	}

	trailerIndex := bytes.Index(payload, []byte("ee")) + 2
	if trailerIndex == 1 {
		return nil, 0, errors.New("invalid piece response")
	}

	dict, err := bencode.Decode(bytes.NewBuffer(payload[:trailerIndex]))
	if err != nil {
		return nil, 0, errors.New("invalid piece response")
	}

	pieceIndex, ok := dict["piece"].(int64)
	if !ok {
		return nil, 0, errors.New("invalid piece response")
	}

	msgType, ok := dict["msg_type"].(int64)
	if !ok || msgType != 1 {
		return nil, 0, errors.New("invalid piece response")
	}

	return payload[trailerIndex:], int(pieceIndex), nil
}

func (mw *metaWire) checkDone() bool {
	for _, b := range mw.pieces {
		if b == nil {
			return false
		}
	}
	return true
}

func (mw *metaWire) preHeader() []byte {
	buf := bytes.NewBuffer(nil)
	buf.WriteByte(19)
	buf.WriteString("BitTorrent protocol")
	buf.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x01})
	return buf.Bytes()
}

func (mw *metaWire) next(ctx context.Context) ([]byte, error) {
	data, err := mw.read(ctx, 4)
	if err != nil {
		return nil, err
	}

	size := binary.BigEndian.Uint32(data)
	data, err = mw.read(ctx, size)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (mw *metaWire) read(ctx context.Context, size uint32) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, context.DeadlineExceeded
	default:
	}

	buf := make([]byte, size)
	_, err := io.ReadFull(mw.conn, buf)
	if err != nil {
		return nil, fmt.Errorf("read %d bytes message failed: %v", size, err)
	}

	return buf, nil
}

func (mw *metaWire) write(ctx context.Context, data []byte) error {
	select {
	case <-ctx.Done():
		return context.DeadlineExceeded
	default:
	}

	buf := bytes.NewBuffer(nil)
	length := int32(len(data))
	binary.Write(buf, binary.BigEndian, length)
	buf.Write(data)
	_, err := mw.conn.Write(buf.Bytes())
	if err != nil {
		return fmt.Errorf("write message failed: %v", err)
	}

	return nil
}
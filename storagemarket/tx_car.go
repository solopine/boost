package storagemarket

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/ipfs/go-cid"
	"strconv"
	"strings"
)

const (
	TxCarKeyPrefix = "TX_CAR_KEY/"
	Separator      = "/"
)

type TxCarInfo struct {
	CarKey    uuid.UUID
	PieceCid  cid.Cid
	PieceSize int64
	CarSize   int64
}

func IsTxCarPath(path string) bool {
	if !strings.HasPrefix(path, TxCarKeyPrefix) {
		return false
	}

	keyStr := path[len(TxCarKeyPrefix):]
	_, err := uuid.Parse(keyStr)
	return err == nil
}

func ParseTxCarInfo(path string) (TxCarInfo, error) {
	var txCarInfo TxCarInfo
	if !strings.HasPrefix(path, TxCarKeyPrefix) {
		return txCarInfo, fmt.Errorf("path has no TxCarKeyPrefix:%s", path)
	}

	parts := strings.Split(path[len(TxCarKeyPrefix):], Separator)
	if len(parts) != 4 {
		return txCarInfo, fmt.Errorf("path is not valid TxCarKeyPrefix with 4 parts:%s", path)
	}

	//
	key, err := uuid.Parse(parts[0])
	if err != nil {
		return txCarInfo, fmt.Errorf("tx car key is invalid:%s", path)
	}
	txCarInfo.CarKey = key

	//
	pieceCid, err := cid.Decode(parts[1])
	if err != nil {
		return txCarInfo, fmt.Errorf("tx PieceCid is invalid:%s", path)
	}
	txCarInfo.PieceCid = pieceCid

	//
	pieceSize, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return txCarInfo, fmt.Errorf("tx PieceSize is invalid:%s", path)
	}
	txCarInfo.PieceSize = pieceSize

	//
	carSize, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return txCarInfo, fmt.Errorf("tx CarSize is invalid:%s", path)
	}
	txCarInfo.CarSize = carSize

	return txCarInfo, nil
}

package txcar

import (
	"fmt"
	txcarlib "github.com/solopine/txcar/txcar"
	"strings"
)

const (
	TxPieceEncodePrefix = "TX_PIECE_DEAL_UUID/"
)

func EncodeInDealUuidStr(txPiece txcarlib.TxPiece, dealUuid string) string {
	str := TxPieceEncodePrefix + dealUuid +
		"/" + txcarlib.BoostPathParser.DeParse(txPiece)
	return str
}

// return (txcarlib.TxPiece, real dealUuidStr, error)
func DecodeFromDealUuidStr(encodedDealUuid string) (*txcarlib.TxPiece, string, error) {
	if !strings.HasPrefix(encodedDealUuid, TxPieceEncodePrefix) {
		return nil, encodedDealUuid, nil
	}

	left := encodedDealUuid[len(TxPieceEncodePrefix):]
	pos := strings.Index(left, "/")

	if pos < 0 {
		return nil, "", fmt.Errorf("encodedDealUuid is not valid:%s", encodedDealUuid)
	}

	realDealUuidStr := left[0:pos]

	txPieceStr := left[pos+1:]
	txPiece, err := txcarlib.BoostPathParser.Parse(txPieceStr)
	if err != nil {
		return nil, "", err
	}

	return &txPiece, realDealUuidStr, nil
}

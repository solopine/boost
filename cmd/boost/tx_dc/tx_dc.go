package tx_dc

import (
	"github.com/filecoin-project/boost/cmd/boost/tx_dc/me"
	"github.com/filecoin-project/boost/cmd/boost/tx_dc/shanghai"
	"github.com/filecoin-project/boost/cmd/boost/tx_dc/share"
	"golang.org/x/xerrors"
	"strings"
)

type TxDcClient int

const (
	Me TxDcClient = iota
	Water
	Xingxing
	Shanghai
)

var (
	txDcClientMap = map[string]TxDcClient{
		"me":       Me,
		"water":    Water,
		"xingxing": Xingxing,
		"shanghai": Shanghai,
	}
)

func ParseTxDcClientString(txDcClientType string) (TxDcClient, bool) {
	c, ok := txDcClientMap[strings.ToLower(txDcClientType)]
	return c, ok
}

func NewTxDcClientHandler(client TxDcClient, dealFilePath string, provider string, txDcClientName string, carRootDir string) (share.TxDcClientHandler, error) {
	switch client {
	case Me:
		return me.NewTxDcClientHandler(dealFilePath, provider, txDcClientName, carRootDir)
	case Shanghai:
		return shanghai.NewTxDcClientHandler(dealFilePath, provider)
	default:
		return nil, xerrors.Errorf("NewTxDcClientHandler not support %s", client)
	}
}

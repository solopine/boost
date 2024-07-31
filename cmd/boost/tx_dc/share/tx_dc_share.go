package share

import (
	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/builtin/v9/market"
	"github.com/google/uuid"
	"github.com/ipfs/go-cid"
	"github.com/solopine/txcar/txcar"
)

type TxDcClientHandler interface {
	DataVersion() txcar.Version
	LdnAddr() string
	Count() int
	DealCar(i int) TxDealCar
	OutputFileName() string
	OutputHeader() string
	OutputLine(i int, dealUuid uuid.UUID, maddr address.Address, rootCid cid.Cid, dealProposal *market.ClientDealProposal) string
}

type TxDealCar struct {
	TxVersion txcar.Version
	PieceCid  cid.Cid
	PieceSize abi.PaddedPieceSize
	RootCid   cid.Cid
}

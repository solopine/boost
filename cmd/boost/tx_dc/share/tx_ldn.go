package share

import (
	"github.com/solopine/txcar/txcar/common"
	"golang.org/x/xerrors"
)

type TxLdn int

const (
	L1 TxLdn = iota + 1
	L2
	L3
	L4
	L5
)

var (
	TxLdnAddrMap = map[TxLdn]string{
		L1: "f1zbwowanywxva2hwrzedxqy7hpgkoavl7nrca4la",
		L2: "f1ifpe2rmywletnc5zwtntrf3tmf5abey4j5hu5ca",
		L3: "f1ucj4c2jvrxlc2pgnebsdhaarrdx4uhptal67etq",
		L4: "f124o7gz4y7ogcqps3kw2ecbkwja6hjqbs2gjefci",
		L5: "f1a5venknwex7jxd6hkeju7odpjpdj322iqdaw3ba",
	}
	TxVersionLdnMap = map[common.TxCarVersion]TxLdn{
		common.V1001: L1,
		common.V1002: L2,
		common.V1003: L3,
		common.V1004: L4,
		common.V1005: L5,
	}
)

func GetLdnAddrByTxVersion(txVersion common.TxCarVersion) (string, error) {
	ldn, ok := TxVersionLdnMap[txVersion]
	if !ok {
		return "", xerrors.Errorf("TxVersionLdnMap Not support: %d", txVersion)
	}

	addr, ok := TxLdnAddrMap[ldn]
	if !ok {
		return "", xerrors.Errorf("TxLdnAddrMap Not support: %d", ldn)
	}

	return addr, nil
}

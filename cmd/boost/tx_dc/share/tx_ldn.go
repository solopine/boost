package share

import (
	"github.com/solopine/txcar/txcar"
	"golang.org/x/xerrors"
)

type TxLdn int

const (
	L1 TxLdn = iota + 1
	L2
	L3
	L4
	L5
	L6
	L7
)

var (
	TxLdnAddrMap = map[TxLdn]string{
		L1: "f1zbwowanywxva2hwrzedxqy7hpgkoavl7nrca4la",
		L2: "f1ifpe2rmywletnc5zwtntrf3tmf5abey4j5hu5ca",
		L3: "f1ucj4c2jvrxlc2pgnebsdhaarrdx4uhptal67etq",
		L4: "f124o7gz4y7ogcqps3kw2ecbkwja6hjqbs2gjefci",
		L5: "f1a5venknwex7jxd6hkeju7odpjpdj322iqdaw3ba",
		L6: "f1vfukygt43b2d5nvzlvokcbok2pe4pykiky3mouy",
		L7: "f1wjl22ez6dvsxlyfiivqil62ziginn2ytmn7b6uy",
	}
	TxVersionLdnMap = map[txcar.Version]TxLdn{
		txcar.V1001: L1,
		txcar.V1002: L2,
		txcar.V1003: L3,
		txcar.V1004: L4,
		txcar.V1005: L5,

		txcar.Version(2001): L1,
		txcar.Version(2002): L2,
		txcar.Version(2003): L3,
		txcar.Version(2004): L4,
		txcar.Version(2005): L5,
		txcar.Version(2006): L6,
		txcar.Version(2007): L7,
	}
)

func GetLdnAddrByTxVersion(txVersion txcar.Version) (string, error) {
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

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
	L8
	L9
	L10
	L11
	L12
	L13
	L14
	L15
	L16
	L17
	L18
)

var (
	TxLdnAddrMap = map[TxLdn]string{
		L1:  "f1zbwowanywxva2hwrzedxqy7hpgkoavl7nrca4la",
		L2:  "f1ifpe2rmywletnc5zwtntrf3tmf5abey4j5hu5ca",
		L3:  "f1ucj4c2jvrxlc2pgnebsdhaarrdx4uhptal67etq",
		L4:  "f124o7gz4y7ogcqps3kw2ecbkwja6hjqbs2gjefci",
		L5:  "f1a5venknwex7jxd6hkeju7odpjpdj322iqdaw3ba",
		L6:  "f1vfukygt43b2d5nvzlvokcbok2pe4pykiky3mouy",
		L7:  "f1wjl22ez6dvsxlyfiivqil62ziginn2ytmn7b6uy",
		L8:  "f1w5cpqgl7saw7vjabbxwfdp3hugca5drp4sseubq",
		L9:  "f1hzlscedx7iiloyb5aossps5ljmytl4hpiao5x3i",
		L10: "f1srtc34se2lgufylq35mgfkegpgdltdiyziw5diq",
		L11: "f167xdmss3bxsa4po5hkshe7lkglzxyuc4rbst43a",
		L12: "f1vidm3ytrx67twx7e5tnhhtjvbw5aomnnguj4ihy",
		L13: "f1em7yhgdfjccx52rjlonotj4tqerb4xlyi24msdi",
		L14: "f1l7pvebfns5gavlcxnktcc2dop7fco4aqb72et7a",
		L15: "f16k5n2uqxp3ly5xqnwd6gxyl6xbwzblfau2p3lci",
		L16: "f1pxfsz24cyrvohytewt5cj5slpias7jxvgo42gsq",
		L17: "f1mokx5ogircmhlbijlthsl2rzb4rhflkvzo77yuy",
		L18: "f1lurvvlzrl6ljo64jb7wpojbykjmsw4ln6cdvmfq",
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
		txcar.Version(2008): L8,
		txcar.Version(2009): L9,
		txcar.Version(2010): L10,
		txcar.Version(2011): L11,
		txcar.Version(2012): L12,
		txcar.Version(2013): L13,
		txcar.Version(2014): L14,
		txcar.Version(2015): L15,
		txcar.Version(2016): L16,
		txcar.Version(2017): L17,
		txcar.Version(2018): L18,
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

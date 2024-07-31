package txcar

import (
	"context"
	"database/sql"
	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/lotus/storage/sealer/storiface"
	"github.com/google/uuid"
	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	"github.com/solopine/txcar/txcar"
)

var log = logging.Logger("boost-txcar")

func ParseTxPiece(path string) (*txcar.TxPiece, error) {
	p, err := txcar.BoostPathParser.Parse(path)
	return &p, err
}

func AddTxPieceToDb(ctx context.Context, db *sql.DB, txPiece *txcar.TxPiece) error {
	qry := "INSERT INTO TxPieces(PieceCid, CarKey, PieceSize, CarSize, Version) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING"
	_, err := db.ExecContext(ctx, qry, txPiece.PieceCid.String(), txPiece.CarKey.String(), txPiece.PieceSize, txPiece.CarSize, txPiece.Version)
	if err != nil {
		return err
	}
	return nil
}

func GetTxPieceFromDb(ctx context.Context, db *sql.DB, pieceCid cid.Cid) (*txcar.TxPiece, error) {
	if db == nil {
		// for boost-http
		return nil, nil
	}
	var carKeyStr string
	var pieceSize uint64
	var carSize uint64
	var version int

	qry := "SELECT CarKey, PieceSize, CarSize, Version FROM TxPieces WHERE PieceCid=?"

	err := db.QueryRowContext(ctx, qry, pieceCid.String()).Scan(&carKeyStr, &pieceSize, &carSize, &version)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	carKey, err := uuid.Parse(carKeyStr)
	if err != nil {
		return nil, err
	}
	return &txcar.TxPiece{
		Version:   txcar.Version(version),
		CarKey:    carKey,
		PieceCid:  pieceCid,
		PieceSize: abi.PaddedPieceSize(pieceSize),
		CarSize:   abi.UnpaddedPieceSize(carSize),
	}, nil
}

func GetTxPieceFromDbBySector(ctx context.Context, db *sql.DB, sector storiface.SectorRef) (*txcar.TxPiece, error) {
	if db == nil {
		// for boost-http
		return nil, nil
	}

	var pieceCidStr string
	maddress, err := address.NewIDAddress(uint64(sector.ID.Miner))
	if err != nil {
		return nil, err
	}

	qry := "SELECT PieceCID FROM DirectDeals WHERE ProviderAddress=? AND SectorID=?"
	err = db.QueryRowContext(ctx, qry, maddress.String(), sector.ID.Number).Scan(&pieceCidStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	pieceCid, err := cid.Decode(pieceCidStr)
	if err != nil {
		return nil, err
	}

	return GetTxPieceFromDb(ctx, db, pieceCid)
}

CREATE TABLE TxPieces (
    PieceCid TEXT,
    CarKey TEXT,
    PieceSize BIGINT,
    CarSize BIGINT,
    Version INT,
    PRIMARY KEY (PieceCid)
) WITHOUT ROWID;
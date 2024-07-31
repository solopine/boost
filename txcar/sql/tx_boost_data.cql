CREATE TABLE TxCarPieces (
   PieceCid TEXT,
   CarKey TEXT,
   PieceSize BIGINT,
   CarSize BIGINT,
   Version INT,
   CreatedAt TIMESTAMP,
   PRIMARY KEY (PieceCid)
);


CREATE TABLE TxPieceBlockOffsetSizeV1 (
    PayloadMultihash BLOB,
    BlockOffset BIGINT,
    BlockSize BIGINT,
    PRIMARY KEY (PayloadMultihash)
);
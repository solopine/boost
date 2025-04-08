package yugabyte

import (
	"context"
	_ "embed"
	"fmt"
	"github.com/filecoin-project/boost/txcar"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/google/uuid"
	txcarlib "github.com/solopine/txcar/txcar"
	"sync"
	"time"

	"github.com/filecoin-project/boost/extern/boostd-data/metrics"
	"github.com/filecoin-project/boost/extern/boostd-data/model"
	"github.com/filecoin-project/boost/extern/boostd-data/shared/tracing"
	"github.com/filecoin-project/boost/extern/boostd-data/svc/types"
	"github.com/filecoin-project/go-address"
	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	mh "github.com/multiformats/go-multihash"
	"github.com/yugabyte/gocql"
	"github.com/yugabyte/pgx/v4/pgxpool"
	"go.opencensus.io/stats"
	"go.opencensus.io/tag"
	"golang.org/x/sync/errgroup"
)

var log = logging.Logger("boostd-data-yb")

// The current piece metadata version. This version will be used when doing
// data migrations (migrations are not yet implemented in version 1).
const pieceMetadataVersion = "1"

const defaultKeyspace = "idx"

const CqlTimeout = 60

// The Cassandra driver has a 50k limit on batch statements. Keeping
// batch size small makes sure we're under the limit.
const InsertBatchSize = 10_000
const MaxInsertBatchSize = 50_000

const InsertConcurrency = 4

type DBSettings struct {
	// The cassandra hosts to connect to
	Hosts []string
	// The cassandra password to use
	Username string
	// The cassandra password to use
	Password string
	// The postgres connect string
	ConnectString string
	// The number of threads to use when inserting into the PayloadToPieces index
	PayloadPiecesParallelism int
	// CQL timeout in seconds
	CQLTimeout int
	// Number of records per insert batch
	InsertBatchSize int
	// Number of concurrent inserts to split AddIndex calls too
	InsertConcurrency int
}

type StoreOpt func(*Store)

// Use the given keyspace for all operations against the cassandra interface
func WithCassandraKeyspace(ks string) StoreOpt {
	return func(s *Store) {
		s.cluster.Keyspace = ks
	}
}

type Store struct {
	settings  DBSettings
	cluster   *gocql.ClusterConfig
	migrator  *Migrator
	session   *gocql.Session
	db        *pgxpool.Pool
	startOnce sync.Once
	ctx       context.Context
}

var _ types.ServiceImpl = (*Store)(nil)

func NewStore(settings DBSettings, migrator *Migrator, opts ...StoreOpt) *Store {
	if settings.PayloadPiecesParallelism == 0 {
		settings.PayloadPiecesParallelism = 16
	}
	if settings.InsertBatchSize == 0 {
		settings.InsertBatchSize = InsertBatchSize
	}
	if settings.InsertBatchSize > MaxInsertBatchSize {
		settings.InsertBatchSize = MaxInsertBatchSize
	}
	if settings.InsertConcurrency == 0 {
		settings.InsertConcurrency = InsertConcurrency
	}

	cluster := NewCluster(settings)
	cluster.Keyspace = defaultKeyspace
	s := &Store{
		settings: settings,
		cluster:  cluster,
		migrator: migrator,
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

func (s *Store) Start(ctx context.Context) error {
	var startErr error
	s.startOnce.Do(func() {
		// Create cassandra keyspace
		err := s.CreateKeyspace(ctx)
		if err != nil {
			startErr = fmt.Errorf("creating cassandra keyspace %s: %w", s.cluster.Keyspace, err)
			return
		}

		// Create connection to cassandra interface (with new keyspace)
		session, err := s.cluster.CreateSession()
		if err != nil {
			startErr = fmt.Errorf("creating yugabyte cluster: %w", err)
			return
		}
		s.session = session

		// Create connection pool to postgres interface
		db, err := pgxpool.Connect(ctx, s.settings.ConnectString)
		if err != nil {
			startErr = fmt.Errorf("connecting to database: %w", err)
			return
		}
		s.db = db
		s.ctx = ctx

		// Create tables
		startErr = s.Create(ctx)
	})

	return startErr
}

func (s *Store) AddDealForPiece(ctx context.Context, pieceCid cid.Cid, dealInfo model.DealInfo) error {
	ctx, span := tracing.Tracer.Start(ctx, "store.add_deal_for_piece")
	defer span.End()
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.add_deal_for_pieces"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailureAddDealForPieceCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessAddDealForPieceCount.M(1))
		}
	}()

	txPiece, realDealUuidStr, err := txcar.DecodeFromDealUuidStr(dealInfo.DealUuid)
	if err != nil {
		return err
	}
	if txPiece != nil {
		// do txpiece
		err := s.addTxPieceToIdxDb(ctx, *txPiece)
		if err != nil {
			return fmt.Errorf("AddTxPieceToIdxDb - inserting deal %s for piece %s", dealInfo.DealUuid, pieceCid)
		}
	}
	dealInfo.DealUuid = realDealUuidStr

	err = s.createPieceMetadata(ctx, pieceCid)
	if err != nil {
		return err
	}

	qry := `INSERT INTO PieceDeal ` +
		`(DealUuid, PieceCid, IsLegacy, ChainDealID, MinerAddr, SectorID, PieceOffset, PieceLength, CarLength, IsDirectDeal) ` +
		`VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ` +
		`IF NOT EXISTS`
	err = s.session.Query(qry,
		dealInfo.DealUuid, pieceCid.Bytes(), dealInfo.IsLegacy, dealInfo.ChainDealID, dealInfo.MinerAddr.String(),
		dealInfo.SectorID, dealInfo.PieceOffset, dealInfo.PieceLength, dealInfo.CarLength, dealInfo.IsDirectDeal).
		WithContext(ctx).Exec()
	if err != nil {
		return fmt.Errorf("inserting deal %s for piece %s", dealInfo.DealUuid, pieceCid)
	}

	failureMetrics = false
	return nil
}

func (s *Store) createPieceMetadata(ctx context.Context, pieceCid cid.Cid) error {
	qry := `INSERT INTO PieceMetadata (PieceCid, Version, CreatedAt) VALUES (?, ?, ?) IF NOT EXISTS`
	err := s.session.Query(qry, pieceCid.String(), pieceMetadataVersion, time.Now()).WithContext(ctx).Exec()
	if err != nil {
		return fmt.Errorf("inserting piece metadata for piece %s: %w", pieceCid, err)
	}

	return nil
}

func (s *Store) GetOffsetSize(ctx context.Context, pieceCid cid.Cid, hash mh.Multihash) (*model.OffsetSize, error) {
	ctx, span := tracing.Tracer.Start(ctx, "store.get_offset_size")
	defer span.End()
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.get_offset_size"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailureGetOffsetSizeCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessGetOffsetSizeCount.M(1))
		}
	}()

	var offset, size uint64

	txPiece, err := s.getTxPieceFromIdxDb(ctx, pieceCid)
	if err != nil {
		return nil, err
	}
	if txPiece != nil && txPiece.Version == txcarlib.V1 {
		offset, size, err = s.getTxOffsetSizeV1(ctx, hash, txPiece.Version)
		if err != nil {
			return nil, err
		}
	} else {
		qry := `SELECT BlockOffset, BlockSize FROM PieceBlockOffsetSize WHERE PieceCid = ? AND PayloadMultihash = ?`
		err := s.session.Query(qry, pieceCid.Bytes(), hash).WithContext(ctx).Scan(&offset, &size)
		if err != nil {
			err = normalizePieceCidError(pieceCid, err)
			return nil, fmt.Errorf("getting offset / size: %w", err)
		}
	}

	failureMetrics = false
	return &model.OffsetSize{Offset: offset, Size: size}, nil
}

// Get piece metadata with deals
func (s *Store) GetPieceMetadata(ctx context.Context, pieceCid cid.Cid) (model.Metadata, error) {
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.get_piece_metadata"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailureGetPieceMetadataCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessGetPieceMetadataCount.M(1))
		}
	}()

	md, err := s.getPieceMetadata(ctx, pieceCid)
	if err != nil {
		return md, err
	}

	deals, err := s.GetPieceDeals(ctx, pieceCid)
	if err != nil {
		return md, fmt.Errorf("getting deals for piece %s: %w", pieceCid, err)
	}
	md.Deals = deals

	failureMetrics = false
	return md, nil
}

// Get the piece metadata without deal information
func (s *Store) getPieceMetadata(ctx context.Context, pieceCid cid.Cid) (model.Metadata, error) {
	ctx, span := tracing.Tracer.Start(ctx, "store.get_piece_metadata")
	defer span.End()

	// Get piece metadata
	var md model.Metadata
	qry := `SELECT Version, IndexedAt, CompleteIndex FROM PieceMetadata WHERE PieceCid = ?`
	err := s.session.Query(qry, pieceCid.String()).WithContext(ctx).
		Scan(&md.Version, &md.IndexedAt, &md.CompleteIndex)
	if err != nil {
		err = normalizePieceCidError(pieceCid, err)
		return md, fmt.Errorf("getting piece metadata: %w", err)
	}

	return md, nil
}

func (s *Store) GetPieceDeals(ctx context.Context, pieceCid cid.Cid) ([]model.DealInfo, error) {
	ctx, span := tracing.Tracer.Start(ctx, "store.get_piece_deals")
	defer span.End()
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.get_piece_deals"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailureGetPieceDealsCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessGetPieceDealsCount.M(1))
		}
	}()

	// Get deals for piece
	qry := `SELECT DealUuid, IsLegacy, ChainDealID, MinerAddr, ` +
		`SectorID, PieceOffset, PieceLength, CarLength, IsDirectDeal ` +
		`FROM PieceDeal WHERE PieceCid = ?`
	iter := s.session.Query(qry, pieceCid.Bytes()).WithContext(ctx).Iter()

	var deals []model.DealInfo
	var deal model.DealInfo
	var minerAddr string
	for iter.Scan(&deal.DealUuid, &deal.IsLegacy, &deal.ChainDealID, &minerAddr,
		&deal.SectorID, &deal.PieceOffset, &deal.PieceLength, &deal.CarLength, &deal.IsDirectDeal) {

		ma, err := address.NewFromString(minerAddr)
		if err != nil {
			return nil, fmt.Errorf("parsing miner address for piece %s / deal %s: %w", pieceCid, deal.DealUuid, err)
		}

		deal.MinerAddr = ma
		deals = append(deals, deal)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("getting piece deals: %w", err)
	}

	// For correctness, we should always return a not found error if there is
	// no piece with the piece cid
	if len(deals) == 0 {
		_, err := s.getPieceMetadata(ctx, pieceCid)
		if err != nil {
			return nil, err
		}
	}

	failureMetrics = false
	return deals, nil
}

// Get all pieces that contain a multihash (used when retrieving by payload CID)
func (s *Store) PiecesContainingMultihash(ctx context.Context, m mh.Multihash) ([]cid.Cid, error) {
	ctx, span := tracing.Tracer.Start(ctx, "store.pieces_containing_multihash")
	defer span.End()
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.pieces_containing_multihash"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailurePiecesContainingMultihashCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessPiecesContainingMultihashCount.M(1))
		}
	}()

	log.Infow("----PiecesContainingMultihash", "m", m.String())
	// first check if it is txcar
	{
		txPcids, err := s.TxPiecesContainingMultihash(ctx, m)
		if err != nil {
			return nil, err
		}
		if txPcids != nil {
			failureMetrics = false
			return txPcids, nil
		}
		// not txcar
	}

	// Get all piece cids referred to by the multihash
	pcids := make([]cid.Cid, 0, 1)
	var bz []byte
	qry := `SELECT PieceCid FROM PayloadToPieces WHERE PayloadMultihash = ?`
	iter := s.session.Query(qry, trimMultihash(m)).WithContext(ctx).Iter()
	for iter.Scan(&bz) {
		pcid, err := cid.Parse(bz)
		if err != nil {
			return nil, fmt.Errorf("parsing piece cid: %w", err)
		}
		pcids = append(pcids, pcid)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("getting pieces containing multihash %s: %w", m, err)
	}

	// No pieces found for multihash, return a "not found" error
	if len(pcids) == 0 {
		return nil, normalizeMultihashError(m, types.ErrNotFound)
	}

	failureMetrics = false
	return pcids, nil
}

func (s *Store) GetIndex(ctx context.Context, pieceCid cid.Cid) (<-chan types.IndexRecord, error) {
	ctx, span := tracing.Tracer.Start(ctx, "store.get_index")
	defer span.End()
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.get_index"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailureGetIndexCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessGetIndexCount.M(1))
		}
	}()

	log.Infow("----GetIndex", "pieceCid", pieceCid.String())
	txPiece, err := s.getTxPieceFromIdxDb(ctx, pieceCid)
	if err != nil {
		return nil, err
	}

	var iter *gocql.Iter
	if txPiece != nil && txPiece.Version == txcarlib.V1 {
		log.Infow("----GetIndex", "txPiece", txPiece)
		iter, err = s.getTxOffsetSizeIter(ctx, txPiece.Version)
		if err != nil {
			return nil, err
		}
	} else {
		qry := `SELECT PayloadMultihash, BlockOffset, BlockSize FROM PieceBlockOffsetSize WHERE PieceCid = ?`
		iter = s.session.Query(qry, pieceCid.Bytes()).WithContext(ctx).Iter()
	}

	scannedRecordCh := make(chan struct{}, 1)
	records := make(chan types.IndexRecord)
	go func() {
		defer close(scannedRecordCh)
		defer close(records)

		var payloadMHBz []byte
		var offset, size uint64
		for iter.Scan(&payloadMHBz, &offset, &size) {
			// The scan was successful, which means there is at least one
			// record
			select {
			case scannedRecordCh <- struct{}{}:
			default:
			}

			// Parse the multihash bytes
			_, pmh, err := mh.MHFromBytes(payloadMHBz)
			if err != nil {
				records <- types.IndexRecord{Error: err}
				return
			}
			records <- types.IndexRecord{
				Record: model.Record{
					Cid: cid.NewCidV1(cid.Raw, pmh),
					OffsetSize: model.OffsetSize{
						Offset: offset,
						Size:   size,
					},
				},
			}
		}
		if err := iter.Close(); err != nil {
			err = fmt.Errorf("getting piece index for piece %s: %w", pieceCid, err)
			records <- types.IndexRecord{Error: err}
		}
	}()

	// Check if there were any records for this piece cid
	var pieceHasRecords bool
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case _, pieceHasRecords = <-scannedRecordCh:
	}

	if !pieceHasRecords {
		// For correctness, we should always return a not found error if there
		// is no piece with the piece cid. Call getPieceMetadata which returns
		// not found if it can't find the piece.
		_, err := s.getPieceMetadata(ctx, pieceCid)
		if err != nil {
			return nil, err
		}
	}

	failureMetrics = false
	return records, nil
}

func (s *Store) AddIndex(ctx context.Context, pieceCid cid.Cid, recs []model.Record, isCompleteIndex bool) <-chan types.AddIndexProgress {
	ctx, span := tracing.Tracer.Start(ctx, "store.add_index")
	defer span.End()
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.add_index"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()

	// Set up the progress channel
	progress := make(chan types.AddIndexProgress, 2)
	if len(recs) == 0 {
		// If there are no records, set progress to 100% and close the channel
		progress <- types.AddIndexProgress{Progress: 1}
		close(progress)
		return progress
	}

	// Start by sending a progress update of zero
	progress <- types.AddIndexProgress{Progress: 0}
	lastUpdateTime := time.Now()

	var lastUpdateValue *float64
	updateProgress := func(prg float64) {
		// Don't send updates more than once every few seconds
		if time.Since(lastUpdateTime) < 5*time.Second {
			lastUpdateValue = &prg
			return
		}

		// If the channel is full, don't send this progress update, just
		// wait for the next one.
		select {
		case progress <- types.AddIndexProgress{Progress: prg}:
			lastUpdateTime = time.Now()
			lastUpdateValue = nil
		default:
			lastUpdateValue = &prg
		}
	}

	completeProgress := func(err error) {
		var lastProg *types.AddIndexProgress
		if err != nil {
			// If there was an error, send it as the last progress update
			stats.Record(s.ctx, metrics.FailureAddIndexCount.M(1))
			lastProg = &types.AddIndexProgress{Err: err.Error()}
		} else if lastUpdateValue != nil {
			// If there is an outstanding update that hasn't been sent out
			// yet, make sure it gets sent
			lastProg = &types.AddIndexProgress{Progress: *lastUpdateValue}
		}

		if lastProg != nil {
			select {
			case progress <- *lastProg:
			case <-time.After(5 * time.Second):
			}
		}

		// Close the channel
		close(progress)
	}

	go func() {
		// Add a mapping from multihash -> piece cid so that clients can look up
		// which pieces contain a multihash
		err := s.addMultihashesToPieces(ctx, pieceCid, recs, func(addProgress float64) {
			// The first 45% of progress is for adding multihash -> pieces index
			updateProgress(0.45 * addProgress)
		})
		if err != nil {
			completeProgress(err)
			return
		}

		// Add a mapping from piece cid -> offset / size of each block so that
		// clients can get the block info for all blocks in a piece
		err = s.addPieceInfos(ctx, pieceCid, recs, func(addProgress float64) {
			// From 45% - 90% of progress is for adding piece infos
			updateProgress(0.45 + 0.45*addProgress)
		})
		if err != nil {
			completeProgress(err)
			return
		}

		// Ensure the piece metadata exists
		err = s.createPieceMetadata(ctx, pieceCid)
		if err != nil {
			completeProgress(err)
			return
		}
		updateProgress(0.95)

		// Mark indexing as complete for the piece
		qry := `UPDATE PieceMetadata ` +
			`SET IndexedAt = ?, CompleteIndex = ? ` +
			`WHERE PieceCid = ?`
		err = s.session.Query(qry, time.Now(), isCompleteIndex, pieceCid.String()).WithContext(ctx).Exec()
		if err != nil {
			completeProgress(err)
			return
		}
		updateProgress(1)
		completeProgress(nil)
		stats.Record(s.ctx, metrics.SuccessAddIndexCount.M(1))
	}()

	return progress
}

func (s *Store) addMultihashesToPieces(ctx context.Context, pieceCid cid.Cid, recs []model.Record, progress func(addProgress float64)) error {
	ctx, span := tracing.Tracer.Start(ctx, "store.add_index.payloadpiece")
	defer span.End()

	txPiece, err := s.getTxPieceFromIdxDb(ctx, pieceCid)
	if err != nil {
		return err
	}
	if txPiece != nil && txPiece.Version == txcarlib.V1 {
		// for txpiece v1, data already in db
		log.Debugw("addMultihashesToPieces for txPiece", "txPiece", txPiece)
		progress(1.0)
		return nil
	}

	insertPieceOffsetsQry := `INSERT INTO PayloadToPieces (PayloadMultihash, PieceCid) VALUES (?, ?)`
	pieceCidBytes := pieceCid.Bytes()

	threadBatch := len(recs) / s.settings.InsertConcurrency // split the slice into go-routine batches

	if threadBatch == 0 {
		threadBatch = len(recs)
	}

	log.Debugw("addMultihashesToPieces call", "threadBatch", threadBatch, "len(recs)", len(recs))

	var eg errgroup.Group
	for i := 0; i < len(recs); i += threadBatch {
		i := i
		j := i + threadBatch
		if j >= len(recs) {
			j = len(recs)
		}

		// Process batch recs[i:j]

		eg.Go(func() error {
			var batch *gocql.Batch
			recsb := recs[i:j]
			for allIdx, rec := range recsb {
				if batch == nil {
					batch = s.session.NewBatch(gocql.UnloggedBatch).WithContext(ctx)
					batch.Entries = make([]gocql.BatchEntry, 0, s.settings.InsertBatchSize)
				}

				batch.Entries = append(batch.Entries, gocql.BatchEntry{
					Stmt:       insertPieceOffsetsQry,
					Args:       []interface{}{trimMultihash(rec.Cid.Hash()), pieceCidBytes},
					Idempotent: true,
				})

				if allIdx == len(recsb)-1 || len(batch.Entries) == s.settings.InsertBatchSize {
					err := func() error {
						defer func(start time.Time) {
							log.Debugw("addMultihashesToPieces executeBatch", "took", time.Since(start), "entries", len(batch.Entries))
						}(time.Now())
						err := s.session.ExecuteBatch(batch)
						if err != nil {
							return fmt.Errorf("inserting into PayloadToPieces: %w", err)
						}
						return nil
					}()
					if err != nil {
						return err
					}
					batch = nil

					// emit progress only from batch 0
					if i == 0 {
						numberOfGoroutines := len(recs)/threadBatch + 1
						progress(float64(numberOfGoroutines) * float64(allIdx+1) / float64(len(recs)))
					}
				}
			}
			return nil
		})
	}

	err = eg.Wait()
	if err != nil {
		return err
	}
	return nil
}

func (s *Store) addPieceInfos(ctx context.Context, pieceCid cid.Cid, recs []model.Record, progress func(addProgress float64)) error {
	ctx, span := tracing.Tracer.Start(ctx, "store.add_index.pieceinfo")
	defer span.End()

	txPiece, err := s.getTxPieceFromIdxDb(ctx, pieceCid)
	if err != nil {
		return err
	}
	if txPiece != nil && txPiece.Version == txcarlib.V1 {
		// for txpiece, data already in db
		log.Debugw("addPieceInfos for txPiece", "txPiece", txPiece)
		progress(1.0)
		return nil
	}

	insertPieceOffsetsQry := `INSERT INTO PieceBlockOffsetSize (PieceCid, PayloadMultihash, BlockOffset, BlockSize) VALUES (?, ?, ?, ?)`
	pieceCidBytes := pieceCid.Bytes()

	threadBatch := len(recs) / s.settings.InsertConcurrency // split the slice into go-routine batches

	if threadBatch == 0 {
		threadBatch = len(recs)
	}

	log.Debugw("addPieceInfos call", "threadBatch", threadBatch, "len(recs)", len(recs))

	var eg errgroup.Group
	for i := 0; i < len(recs); i += threadBatch {
		i := i
		j := i + threadBatch
		if j >= len(recs) {
			j = len(recs)
		}

		// Process batch recs[i:j]

		eg.Go(func() error {
			var batch *gocql.Batch
			recsb := recs[i:j]
			for allIdx, rec := range recsb {
				if batch == nil {
					batch = s.session.NewBatch(gocql.UnloggedBatch).WithContext(ctx)
					batch.Entries = make([]gocql.BatchEntry, 0, s.settings.InsertBatchSize)
				}

				batch.Entries = append(batch.Entries, gocql.BatchEntry{
					Stmt:       insertPieceOffsetsQry,
					Args:       []any{pieceCidBytes, rec.Cid.Hash(), rec.Offset, rec.Size},
					Idempotent: true,
				})

				if allIdx == len(recsb)-1 || len(batch.Entries) == s.settings.InsertBatchSize {
					err := func() error {
						defer func(start time.Time) {
							log.Debugw("addPieceInfos executeBatch", "took", time.Since(start), "entries", len(batch.Entries))
						}(time.Now())

						err := s.session.ExecuteBatch(batch)
						if err != nil {
							return fmt.Errorf("executing offset / size batch insert for piece %s: %w", pieceCid, err)
						}
						return nil
					}()
					if err != nil {
						return err
					}
					batch = nil

					// emit progress only from batch 0
					if i == 0 {
						numberOfGoroutines := len(recs)/threadBatch + 1
						progress(float64(numberOfGoroutines) * float64(allIdx+1) / float64(len(recs)))
					}
				}
			}
			return nil
		})
	}

	err = eg.Wait()
	if err != nil {
		return err
	}

	return nil
}

func (s *Store) IsCompleteIndex(ctx context.Context, pieceCid cid.Cid) (bool, error) {
	ctx, span := tracing.Tracer.Start(ctx, "store.is_incomplete_index")
	defer span.End()
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.is_complete_index"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailureIsCompleteIndexCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessIsCompleteIndexCount.M(1))
		}
	}()

	md, err := s.getPieceMetadata(ctx, pieceCid)
	if err != nil {
		return false, err
	}

	failureMetrics = false
	return md.CompleteIndex, nil
}

func (s *Store) IsIndexed(ctx context.Context, pieceCid cid.Cid) (bool, error) {
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.is_indexed"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailureIsIndexedCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessIsIndexedCount.M(1))
		}
	}()

	t, err := s.IndexedAt(ctx, pieceCid)
	if err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, err
	}
	failureMetrics = false
	return !t.IsZero(), nil
}

func (s *Store) IndexedAt(ctx context.Context, pieceCid cid.Cid) (time.Time, error) {
	ctx, span := tracing.Tracer.Start(ctx, "store.indexed_at")
	defer span.End()
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.indexed_at"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailureIndexedAtCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessIndexedAtCount.M(1))
		}
	}()

	md, err := s.getPieceMetadata(ctx, pieceCid)
	if err != nil {
		if isNotFoundErr(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}

	failureMetrics = false
	return md.IndexedAt, nil
}

func (s *Store) ListPieces(ctx context.Context) ([]cid.Cid, error) {
	ctx, span := tracing.Tracer.Start(ctx, "store.list_pieces")
	defer span.End()
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.list_pieces"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailureListPiecesCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessListPiecesCount.M(1))
		}
	}()

	iter := s.session.Query("SELECT PieceCid FROM PieceMetadata").WithContext(ctx).Iter()
	var pcids []cid.Cid
	var cstr string
	for iter.Scan(&cstr) {
		c, err := cid.Parse(cstr)
		if err != nil {
			return nil, fmt.Errorf("parsing piece cid: %w", err)
		}

		pcids = append(pcids, c)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("getting piece cids: %w", err)
	}

	failureMetrics = false
	return pcids, nil
}

// RemoveDealForPiece removes Single deal for pieceCID.
func (s *Store) RemoveDealForPiece(ctx context.Context, pieceCid cid.Cid, dealId string) error {
	ctx, span := tracing.Tracer.Start(ctx, "store.remove_deal_for_piece")
	defer span.End()
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.remove_deal_for_piece"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailureRemoveDealForPieceCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessRemoveDealForPieceCount.M(1))
		}
	}()

	qry := `DELETE FROM PieceDeal WHERE DealUuid = ?`
	err := s.session.Query(qry, dealId).WithContext(ctx).Exec()
	if err != nil {
		return fmt.Errorf("removing deal %s for piece %s: %w", dealId, pieceCid, err)
	}

	dls, err := s.GetPieceDeals(ctx, pieceCid)
	if err != nil {
		return fmt.Errorf("getting remaining deals in remove deal for piece %s: %w", pieceCid, err)
	}

	if len(dls) > 0 {
		return nil
	}

	txPiece, err := s.getTxPieceFromIdxDb(ctx, pieceCid)
	if err != nil {
		return err
	}
	if txPiece != nil && txPiece.Version == txcarlib.V1 {
		// for txpiece, need remove piece
		log.Debugw("RemoveDealForPiece for txPiece", "txPiece", txPiece)
		qry = `DELETE FROM TxCarPieces WHERE PieceCid = ?`
		err = s.session.Query(qry, txPiece.PieceCid.String()).WithContext(ctx).Exec()
		if err != nil {
			return fmt.Errorf("remove TxCarPieces for piece %s: %w", txPiece.PieceCid.String(), err)
		}
	}

	err = s.RemoveIndexes(ctx, pieceCid)
	if err != nil {
		return fmt.Errorf("removing deal: %w", err)
	}

	err = s.RemovePieceMetadata(ctx, pieceCid)
	if err != nil {
		return fmt.Errorf("removing deal: %w", err)
	}

	failureMetrics = false
	return nil
}

// RemovePieceMetadata removes all Metadata for pieceCID.
func (s *Store) RemovePieceMetadata(ctx context.Context, pieceCid cid.Cid) error {
	ctx, span := tracing.Tracer.Start(ctx, "store.remove_piece_metadata")
	defer span.End()
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.remove_piece_metadata"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailureRemovePieceMetadataCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessRemovePieceMetadataCount.M(1))
		}
	}()

	qry := `DELETE FROM PieceMetadata WHERE PieceCid = ?`
	err := s.session.Query(qry, pieceCid.String()).WithContext(ctx).Exec()
	if err != nil {
		return fmt.Errorf("removing piece metadata for piece %s: %w", pieceCid, err)
	}

	failureMetrics = false
	return nil
}

// RemoveIndexes removes all multihash -> piece cid mappings, and all
// offset / size information for the piece.
func (s *Store) RemoveIndexes(ctx context.Context, pieceCid cid.Cid) error {
	ctx, span := tracing.Tracer.Start(ctx, "store.remove_indexes")
	defer span.End()
	ctx, _ = tag.New(ctx, tag.Upsert(metrics.Endpoint, "yb.remove_indexes"))
	stop := metrics.Timer(ctx, metrics.APIRequestDuration)
	defer stop()
	failureMetrics := true
	defer func() {
		if failureMetrics {
			stats.Record(s.ctx, metrics.FailureRemoveIndexesCount.M(1))
		} else {
			stats.Record(s.ctx, metrics.SuccessRemoveIndexesCount.M(1))
		}
	}()

	txPiece, err := s.getTxPieceFromIdxDb(ctx, pieceCid)
	if err != nil {
		return err
	}
	if txPiece != nil && txPiece.Version == txcarlib.V1 {
		// for txpiece, don't need remove anything
		log.Debugw("RemoveIndexes for txPiece", "txPiece", txPiece)
		failureMetrics = false
		return nil
	}

	// Get multihashes for piece
	recs, err := s.GetIndex(ctx, pieceCid)
	if err != nil {
		return fmt.Errorf("removing indexes for piece %s: getting recs: %w", pieceCid, err)
	}

	// Delete from multihash -> piece cids index and PieceBlockOffsetSize
	var eg errgroup.Group
	for i := 0; i < s.settings.PayloadPiecesParallelism; i++ {
		eg.Go(func() error {
			for ctx.Err() == nil {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case rec, ok := <-recs:
					if !ok {
						// Finished adding all the queued items, exit the thread
						return nil
					}

					multihashBytes := rec.Cid.Hash()
					q := `DELETE FROM PayloadToPieces WHERE PayloadMultihash = ? AND PieceCid = ?`
					err := s.session.Query(q, trimMultihash(multihashBytes), pieceCid.Bytes()).Exec()
					if err != nil {
						return fmt.Errorf("deleting from PayloadToPieces: %w", err)
					}
					q = `DELETE FROM PieceBlockOffsetSize WHERE PayloadMultihash = ? AND PieceCid = ?`
					err = s.session.Query(q, multihashBytes, pieceCid.Bytes()).Exec()
					if err != nil {
						return fmt.Errorf("deleting from PieceBlockOffsetSize: %w", err)
					}
				}
			}

			return ctx.Err()
		})
	}
	err = eg.Wait()
	if err != nil {
		return err
	}

	failureMetrics = false
	return nil
}

// /// Tx car db
func (s *Store) addTxPieceToIdxDb(ctx context.Context, txPiece txcarlib.TxPiece) error {
	qry := `INSERT INTO TxCarPieces (PieceCid, CarKey, PieceSize, CarSize, Version, CreatedAt) VALUES (?, ?, ?, ?, ?, ?) IF NOT EXISTS`
	err := s.session.Query(qry, txPiece.PieceCid.String(), txPiece.CarKey.String(), txPiece.PieceSize, txPiece.CarSize, txPiece.Version, time.Now()).WithContext(ctx).Exec()
	if err != nil {
		return fmt.Errorf("inserting TxCarPieces for piece %s: %w", txPiece.PieceCid.String(), err)
	}
	return nil
}

func (s *Store) getTxPieceFromIdxDb(ctx context.Context, pieceCid cid.Cid) (*txcarlib.TxPiece, error) {

	var carKeyStr string
	var pieceSize uint64
	var carSize uint64
	var version int
	qry := `SELECT CarKey, PieceSize, CarSize, Version FROM TxCarPieces WHERE PieceCid = ?`
	err := s.session.Query(qry, pieceCid.String()).WithContext(ctx).
		Scan(&carKeyStr, &pieceSize, &carSize, &version)
	if err != nil {
		if err == gocql.ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("getTxPieceFromIdxDb: %w", err)
	}
	carKey, err := uuid.Parse(carKeyStr)
	if err != nil {
		return nil, fmt.Errorf("carKey uuid.Parse: %w", err)
	}
	var txPiece txcarlib.TxPiece
	txPiece.PieceCid = pieceCid
	txPiece.CarKey = carKey
	txPiece.PieceSize = abi.PaddedPieceSize(pieceSize)
	txPiece.CarSize = abi.UnpaddedPieceSize(carSize)
	txPiece.Version = txcarlib.Version(version)
	return &txPiece, nil
}

func (s *Store) TxPiecesContainingMultihash(ctx context.Context, m mh.Multihash) ([]cid.Cid, error) {
	// check if it v1 txcar
	log.Infow("----TxPiecesContainingMultihash", "m", m.String())
	version := 0
	if version == 0 {
		var mock int
		qry := `SELECT 1 FROM TxPieceBlockOffsetSizeV1 WHERE PayloadMultihash = ?`
		err := s.session.Query(qry, m).WithContext(ctx).Scan(&mock)
		if err != nil {
			if err != gocql.ErrNotFound {
				return nil, fmt.Errorf("TxPieceBlockOffsetSizeV1: %w", err)
			}
			return nil, nil
		}
		version = 1
		log.Infow("----TxPiecesContainingMultihash is version 1", "m", m.String())
	}

	if version == 0 {
		return nil, nil
	}

	return s.GetTxPiecesByVersion(ctx, txcarlib.Version(version))
}

func (s *Store) GetTxPiecesByVersion(ctx context.Context, version txcarlib.Version) ([]cid.Cid, error) {
	// Get all piece cids referred to by the multihash
	log.Infow("----GetTxPiecesByVersion", "version", version)
	pcids := make([]cid.Cid, 0, 1)
	var bz string
	qry := `SELECT PieceCid FROM TxCarPieces WHERE Version = ?`
	iter := s.session.Query(qry, version).WithContext(ctx).Iter()
	for iter.Scan(&bz) {
		pcid, err := cid.Decode(bz)
		if err != nil {
			return nil, fmt.Errorf("parsing piece cid: %w", err)
		}
		pcids = append(pcids, pcid)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("getting txcar v1 pieces: %w", err)
	}
	return pcids, nil
}

func (s *Store) getTxOffsetSizeV1(ctx context.Context, hash mh.Multihash, version txcarlib.Version) (uint64, uint64, error) {
	var offset, size uint64
	if version == txcarlib.V1 {
		qry := `SELECT BlockOffset, BlockSize FROM TxPieceBlockOffsetSizeV1 WHERE PayloadMultihash = ?`
		err := s.session.Query(qry, hash).WithContext(ctx).Scan(&offset, &size)
		if err != nil {
			return 0, 0, fmt.Errorf("getTxOffsetSize offset / size: %w", err)
		}
		return offset, size, nil
	}

	return 0, 0, fmt.Errorf("getTxOffsetSize version not support. version:%d", version)
}

func (s *Store) getTxOffsetSizeIter(ctx context.Context, version txcarlib.Version) (*gocql.Iter, error) {

	if version == txcarlib.V1 {
		qry := `SELECT PayloadMultihash, BlockOffset, BlockSize FROM TxPieceBlockOffsetSizeV1`
		return s.session.Query(qry).WithContext(ctx).Iter(), nil
	}

	return nil, fmt.Errorf("getTxOffsetSizeQuery version not support. version:%d", version)
}

func (s *Store) GetTxPiece(ctx context.Context, pieceCid cid.Cid) (*txcarlib.TxPiece, error) {
	return s.getTxPieceFromIdxDb(ctx, pieceCid)
}

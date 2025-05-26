package txdc

import (
	"context"
	"fmt"
	"github.com/filecoin-project/boost/cmd/lib/remoteblockstore"
	"github.com/filecoin-project/boost/piecedirectory"
	"github.com/hashicorp/go-multierror"
	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	"golang.org/x/xerrors"
	"io"
	"net/http"
)

var log = logging.Logger("txdc")

type TxRemoteBlockstore struct {
	remoteblockstore.RemoteBlockstoreAPI
	ps *piecedirectory.PieceDirectory

	// http://ip:port
	txdcBaseUrl string
}

func NewTxRemoteBlockstore(ps *piecedirectory.PieceDirectory, txdcBaseUrl string) remoteblockstore.RemoteBlockstoreAPI {
	if txdcBaseUrl == "" {
		log.Warnw("NewTxRemoteBlockstore, txdcBaseUrl is empty, so fallback to PieceDirectory")
		return ps
	}
	return &TxRemoteBlockstore{RemoteBlockstoreAPI: ps, ps: ps, txdcBaseUrl: txdcBaseUrl}
}

func (bs *TxRemoteBlockstore) checkTxdcServerHealthy(ctx context.Context) bool {
	url := bs.txdcBaseUrl + "/remote/health"

	log.Infow("txdc.checkTxdcServerHealthy", "url", url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Errorf("TxRemoteBlockstore.checkTxdcServerHealthy NewRequest. err: %v", err)
		return false
	}

	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Errorf("TxRemoteBlockstore.checkTxdcServerHealthy Do req. err: %v", err)
		return false
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close() // nolint
		log.Errorf("TxRemoteBlockstore.checkTxdcServerHealthy non-200 code: %d", resp.StatusCode)
		return false
	}
	return true
}

func (bs *TxRemoteBlockstore) internalBlockstoreGet(ctx context.Context, c cid.Cid) ([]byte, error) {
	log.Infow("start txdc.internalBlockstoreGet", "c", c.String())
	// Get the pieces that contain the cid
	pieces, err := bs.ps.PiecesContainingMultihash(ctx, c.Hash())

	log.Infow("txdc.internalBlockstoreGet PiecesContainingMultihash", "pieces", len(pieces))

	// Check if it's an identity cid, if it is, return its digest
	if err != nil {
		return nil, xerrors.Errorf("internalBlockstoreGet.PiecesContainingMultihash. %w", err)
	}
	if len(pieces) == 0 {
		return nil, fmt.Errorf("no pieces with cid %s found", c)
	}

	var merr error
	for i, pieceCid := range pieces {
		data, err := func() ([]byte, error) {
			log.Infow("txdc.internalBlockstoreGet process piece", "pieceCid", pieceCid)
			// Get the offset of the block within the piece (CAR file)
			offsetSize, err := bs.ps.GetOffsetSize(ctx, pieceCid, c.Hash())
			if err != nil {
				return nil, fmt.Errorf("getting offset/size for cid %s in piece %s: %w", c, pieceCid, err)
			}

			log.Infow("txdc.GetOffsetSize", "pieceCid", pieceCid, "offsetSize", offsetSize)

			// /remote/piece/{pieceCid}/block/{blockCid}/{offset}/{size}
			url := fmt.Sprintf("%s/remote/piece/%s/block/%s/%d/%d", bs.txdcBaseUrl, pieceCid.String(), c.String(), offsetSize.Offset, offsetSize.Size)
			log.Infow("txdc.NewRequest", "url", url)

			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				return nil, xerrors.Errorf("request: %w", err)
			}

			req = req.WithContext(ctx)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, xerrors.Errorf("do request: %w", err)
			}

			if resp.StatusCode != http.StatusOK {
				resp.Body.Close() // nolint
				return nil, xerrors.Errorf("non-200 code: %d", resp.StatusCode)
			}

			return io.ReadAll(resp.Body)
		}()
		if err != nil {
			if i < 3 {
				merr = multierror.Append(merr, err)
			}
			continue
		}
		return data, nil
	}

	return nil, merr
}

func (bs *TxRemoteBlockstore) BlockstoreGet(ctx context.Context, c cid.Cid) ([]byte, error) {
	log.Infow("txdc.BlockstoreGet", "c", c.String())
	if !bs.checkTxdcServerHealthy(ctx) {
		log.Infow("txdc.checkTxdcServerHealthy false, fall back to original bs.RemoteBlockstoreAPI.BlockstoreGet", "c", c.String())
		return bs.RemoteBlockstoreAPI.BlockstoreGet(ctx, c)
	}

	b, err := bs.internalBlockstoreGet(ctx, c)
	if err != nil {
		log.Warnw("TxRemoteBlockstore.internalBlockstoreGet", "c", c, "err", err)
		return bs.RemoteBlockstoreAPI.BlockstoreGet(ctx, c)
	}
	return b, nil
}

package main

import (
	"fmt"
	"github.com/filecoin-project/boost/txcar"
	txcarlib "github.com/solopine/txcar/txcar"
	"strings"

	bcli "github.com/filecoin-project/boost/cli"
	"github.com/google/uuid"
	"github.com/ipfs/go-cid"
	"github.com/urfave/cli/v2"
)

var importDataTxCmd = &cli.Command{
	Name:      "import-data-tx",
	Usage:     "Import data tx for offline deal made with Boost",
	ArgsUsage: "<proposal CID> <file> or <deal UUID> <file>",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "delete-after-import",
			Usage: "whether to delete the data for the offline deal after the deal has been added to a sector",
			Value: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() < 2 {
			return fmt.Errorf("must specify proposal CID / deal UUID and file path")
		}

		id := cctx.Args().Get(0)
		//tpath := cctx.Args().Get(1)
		//
		//path, err := homedir.Expand(tpath)
		//if err != nil {
		//	return fmt.Errorf("expanding file path: %w", err)
		//}
		//
		//filePath, err := filepath.Abs(path)
		//if err != nil {
		//	return fmt.Errorf("failed get absolute path for file: %w", err)
		//}
		//
		//_, err = os.Stat(filePath)
		//if err != nil {
		//	return fmt.Errorf("opening file %s: %w", filePath, err)
		//}

		txCarInfoStr := cctx.Args().Get(1)
		txCarInfoStr = txcarlib.TxCarKeyPrefix + txCarInfoStr
		txPiece, err := txcar.ParseTxPiece(txCarInfoStr)
		if err != nil {
			return fmt.Errorf("txPiece invalid: %w", err)
		}
		if txPiece == nil {
			return fmt.Errorf("txPiece is nil")
		}

		// Parse the first parameter as a deal UUID or a proposal CID
		var proposalCid *cid.Cid
		dealUuid, err := uuid.Parse(id)
		if err != nil {
			propCid, err := cid.Decode(id)
			if err != nil {
				return fmt.Errorf("could not parse '%s' as deal uuid or proposal cid", id)
			}
			proposalCid = &propCid
		}

		napi, closer, err := bcli.GetBoostAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		// If the user has supplied a signed proposal cid
		deleteAfterImport := cctx.Bool("delete-after-import")
		if proposalCid != nil {

			// Look up the deal in the boost database
			deal, err := napi.BoostDealBySignedProposalCid(cctx.Context, *proposalCid)
			if err != nil {
				// If the error is anything other than a Not Found error,
				// return the error
				if !strings.Contains(err.Error(), "not found") {
					return err
				}

				return fmt.Errorf("cannot find boost deal with proposal cid %s and legacy deals are no olnger supported", proposalCid)
			}

			// Get the deal UUID from the deal
			dealUuid = deal.DealUuid
		}

		// Deal proposal by deal uuid (v1.2.0 deal)
		rej, err := napi.BoostOfflineDealWithData(cctx.Context, dealUuid, txCarInfoStr, deleteAfterImport)
		if err != nil {
			return fmt.Errorf("failed to execute offline deal: %w", err)
		}
		if rej != nil && rej.Reason != "" {
			return fmt.Errorf("offline deal %s rejected: %s", dealUuid, rej.Reason)
		}
		fmt.Printf("Offline deal import for v1.2.0 deal %s scheduled for execution\n", dealUuid)
		return nil
	},
}

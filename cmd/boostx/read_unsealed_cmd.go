package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/filecoin-project/go-commp-utils/writer"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/storage/sealer/fr32"
	"github.com/filecoin-project/lotus/storage/sealer/partialfile"
	"github.com/filecoin-project/lotus/storage/sealer/storiface"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-cidutil/cidenc"
	carv2 "github.com/ipld/go-car/v2"
	"github.com/ipld/go-car/v2/blockstore"
	"github.com/multiformats/go-multibase"
	"github.com/urfave/cli/v2"
	"golang.org/x/xerrors"
)

var readUnsealedCmd = &cli.Command{
	Name:   "read-unsealed",
	Usage:  "",
	Before: before,
	Flags:  []cli.Flag{},
	Action: func(cctx *cli.Context) error {
		//path := "/Users/nonsense/s-t01000-1" // unsealed file
		path := "/Users/nonsense/weirdsector/16581-unsealed" // unsealed file

		ss8MiB := 8 << 20 // 8MiB sector
		_ = ss8MiB

		ss32GiB := 32 << 30 // 32GiB sector
		_ = ss32GiB

		ss64GiB := 64 << 30 // 64GiB sector
		_ = ss64GiB

		//maxPieceSize := abi.PaddedPieceSize(ss8MiB)
		//maxPieceSize := abi.PaddedPieceSize(ss32GiB)
		maxPieceSize := abi.PaddedPieceSize(ss64GiB)

		deals := []struct {
			offset storiface.PaddedByteIndex
			size   abi.PaddedPieceSize
		}{
			{ // first deal
				storiface.PaddedByteIndex(0),
				abi.PaddedPieceSize(32 << 30),
			},
			{ // second deal
				storiface.PaddedByteIndex(abi.PaddedPieceSize(32 << 30).Unpadded()),
				abi.PaddedPieceSize(32 << 30),
			},
		}

		for dealIndex, d := range deals {
			err := func() error {
				defer func(now time.Time) {
					fmt.Println("processing deal/piece took", time.Since(now))
				}(time.Now())

				pf, err := partialfile.OpenPartialFile(maxPieceSize, path)
				if err != nil {
					return err
				}
				defer pf.Close()

				f, err := pf.Reader(d.offset, d.size)
				if err != nil {
					return err
				}

				upr, err := fr32.NewUnpadReader(f, d.size)
				if err != nil {
					return xerrors.Errorf("creating unpadded reader: %w", err)
				}

				w := &writer.Writer{}
				if _, err := io.CopyN(w, upr, int64(d.size.Unpadded())); err != nil {
					return xerrors.Errorf("reading unsealed file: %w", err)
				}

				commp, err := w.Sum()
				if err != nil {
					return fmt.Errorf("computing commP failed: %w", err)
				}

				encoder := cidenc.Encoder{Base: multibase.MustNewEncoder(multibase.Base32)}

				fmt.Println("CommP CID: ", encoder.Encode(commp.PieceCID))
				fmt.Println("Piece size: ", types.NewInt(uint64(commp.PieceSize.Unpadded().Padded())))
				fmt.Println()

				// reset f and upr
				f, err = pf.Reader(d.offset, d.size)
				if err != nil {
					return err
				}

				upr, err = fr32.NewUnpadReaderBuf(f, d.size, make([]byte, 65<<30))
				if err != nil {
					return xerrors.Errorf("creating unpadded reader: %w", err)
				}

				var buff bytes.Buffer
				_, err = io.Copy(&buff, upr)
				if err != nil {
					return err
				}

				readerAt := bytes.NewReader(buff.Bytes())

				opts := []carv2.Option{carv2.ZeroLengthSectionAsEOF(true)}
				rr, err := carv2.NewReader(readerAt, opts...)
				if err != nil {
					return err
				}

				spew.Dump(rr.Inspect(false))

				if dealIndex == 1 { // baga6ea4seaqdztbv3g4gtc3evmtsdimt3rkmpx3bo5rkgt3ph7di3kt6tnhw2la
					bs, err := blockstore.NewReadOnly(readerAt, nil, opts...)
					if err != nil {
						return err
					}

					blkCid, err := cid.Parse("bafybeiecyv2b5qd7rzm7zjuaczbzi6znvrxv3vqnwz43bud7yozlnuhx4m")
					if err != nil {
						return err
					}

					blk, err := bs.Get(context.Background(), blkCid)
					if err != nil {
						return err
					}

					spew.Dump(blk)
				}

				return nil
			}()

			if err != nil {
				return err
			}
		}

		return nil
	},
}

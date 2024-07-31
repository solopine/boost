package main

import (
	"bufio"
	"context"
	"fmt"
	bcli "github.com/filecoin-project/boost/cli"
	clinode "github.com/filecoin-project/boost/cli/node"
	"github.com/filecoin-project/boost/cmd"
	"github.com/filecoin-project/boost/cmd/boost/tx_dc/share"
	"github.com/filecoin-project/boost/cmd/lib"
	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/builtin"
	"github.com/filecoin-project/go-state-types/builtin/v13/datacap"
	verifreg13types "github.com/filecoin-project/go-state-types/builtin/v13/verifreg"
	verifregtypes "github.com/filecoin-project/go-state-types/builtin/v9/verifreg"
	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/build/buildconstants"
	"github.com/filecoin-project/lotus/chain/actors"
	datacap2 "github.com/filecoin-project/lotus/chain/actors/builtin/datacap"
	"github.com/filecoin-project/lotus/chain/actors/builtin/verifreg"
	"github.com/filecoin-project/lotus/chain/types"
	lcli "github.com/filecoin-project/lotus/cli"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	ds_sync "github.com/ipfs/go-datastore/sync"
	"github.com/urfave/cli/v2"
	"golang.org/x/sync/errgroup"
	"golang.org/x/xerrors"
	"os"
	"strconv"
	"strings"
)

var txClientExtendDealCmd = &cli.Command{
	Name:  "tx-extend-claim",
	Usage: "extend claim expiration (TermMax)",
	UsageText: `Extends claim expiration (TermMax).
If the client is the original client, then the claim can be extended up to a maximum of 5 years, and no Datacap is required.
If the client id different then claim can be extended up to maximum 5 years from now and Datacap is required.
`,
	Flags: []cli.Flag{
		&cli.Int64Flag{
			Name:    "term-max",
			Usage:   "The maximum period for which a provider can earn quality-adjusted power for the piece (epochs). Default is 5 years.",
			Aliases: []string{"tmax"},
			Value:   verifreg13types.MaximumVerifiedAllocationTerm,
		},
		&cli.StringFlag{
			Name:  "wallet",
			Usage: "the wallet address that will used to send the message",
		},
		&cli.BoolFlag{
			Name:    "assume-yes",
			Usage:   "automatic yes to prompts; assume 'yes' as answer to all prompts and run non-interactively",
			Aliases: []string{"y", "yes"},
			Value:   true,
		},
		&cli.IntFlag{
			Name:  "confidence",
			Usage: "number of block confirmations to wait for",
			Value: int(buildconstants.MessageConfidence),
		},
		&cli.IntFlag{
			Name:  "batch-size",
			Usage: "number of extend requests per batch. If set incorrectly, this will lead to out of gas error",
			Value: 500,
		},
		cmd.FlagRepo,
	},
	ArgsUsage: "file",
	Before:    before,
	Action: func(cctx *cli.Context) error {

		n, err := clinode.Setup(cctx.String(cmd.FlagRepo.Name))
		if err != nil {
			return err
		}

		wallet := cctx.String("wallet")
		tmax := cctx.Int64("term-max")

		// Tmax can't be more than policy max
		if tmax > verifreg13types.MaximumVerifiedAllocationTerm {
			return fmt.Errorf("specified term-max %d is larger than %d maximum allowed by verified regirty actor policy", tmax, verifreg13types.MaximumVerifiedAllocationTerm)
		}

		gapi, closer, err := lcli.GetGatewayAPI(cctx)
		if err != nil {
			return fmt.Errorf("can't setup gateway connection: %w", err)
		}
		defer closer()

		ctx := bcli.ReqContext(cctx)

		filePath := cctx.Args().First()
		claimMap, err := readClaimsFromFile(filePath)
		if err != nil {
			return fmt.Errorf("readClaimsFromFile: %w", err)
		}

		// Get wallet address from input
		walletAddr, err := n.GetProvidedOrDefaultWallet(ctx, wallet)
		if err != nil {
			return err
		}

		log.Debugw("selected wallet", "wallet", walletAddr)

		msgs, err := createExtendClaimMsg(ctx, gapi, claimMap, walletAddr, abi.ChainEpoch(tmax), cctx.Int("batch-size"))
		if err != nil {
			return err
		}

		// If not msgs are found then no claims can be extended
		if msgs == nil {
			fmt.Println("No eligible claims found")
			return nil
		}

		var mcids []cid.Cid
		ds := ds_sync.MutexWrap(datastore.NewMapDatastore())
		msgCount := len(msgs)
		for i, msg := range msgs {
			mcid, sent, err := lib.SignAndPushToMpool(cctx, ctx, gapi, n, ds, msg)
			if err != nil {
				return err
			}
			if !sent {
				fmt.Printf("message %s with method %s not sent\n", msg.Cid(), msg.Method.String())
				continue
			}

			log.Infof("about to send msg (%d/%d): %s", i, msgCount, mcid.String())
			wait, err := gapi.StateWaitMsg(ctx, mcid, uint64(cctx.Int("confidence")), 2000, true)
			if err != nil {
				return fmt.Errorf("timeout waiting for message to land on chain %s", mcid.String())

			}

			if wait.Receipt.ExitCode.IsError() {
				log.Errorf("failed to execute message %s: %w", mcid.String(), wait.Receipt.ExitCode)
				continue
			}
			log.Infof("msg sent (%d/%d): %s", i, msgCount, mcid.String())
			mcids = append(mcids, mcid)
		}

		// wait for msgs to get mined into a block
		eg := errgroup.Group{}
		eg.SetLimit(10)
		for _, msg := range mcids {
			m := msg
			eg.Go(func() error {
				wait, err := gapi.StateWaitMsg(ctx, m, uint64(cctx.Int("confidence")), 2000, true)
				if err != nil {
					return fmt.Errorf("timeout waiting for message to land on chain %s", m.String())

				}

				if wait.Receipt.ExitCode.IsError() {
					return fmt.Errorf("failed to execute message %s: %w", m.String(), wait.Receipt.ExitCode)
				}
				return nil
			})
		}
		return eg.Wait()
	},
}

var txdcExtendDealCmd = &cli.Command{
	Name:  "txdc-extend-claim",
	Usage: "extend claim expiration (TermMax)",
	UsageText: `Extends claim expiration (TermMax).
If the client is the original client, then the claim can be extended up to a maximum of 5 years, and no Datacap is required.
If the client id different then claim can be extended up to maximum 5 years from now and Datacap is required.
`,
	Flags: []cli.Flag{
		&cli.Int64Flag{
			Name:    "term-max",
			Usage:   "The maximum period for which a provider can earn quality-adjusted power for the piece (epochs). Default is 5 years.",
			Aliases: []string{"tmax"},
			Value:   verifreg13types.MaximumVerifiedAllocationTerm,
		},
		&cli.BoolFlag{
			Name:    "assume-yes",
			Usage:   "automatic yes to prompts; assume 'yes' as answer to all prompts and run non-interactively",
			Aliases: []string{"y", "yes"},
			Value:   true,
		},
		&cli.IntFlag{
			Name:  "confidence",
			Usage: "number of block confirmations to wait for",
			Value: int(buildconstants.MessageConfidence),
		},
		&cli.IntFlag{
			Name:  "batch-size",
			Usage: "number of extend requests per batch. If set incorrectly, this will lead to out of gas error",
			Value: 200,
		},
		&cli.StringSliceFlag{
			Name:     "miners",
			Usage:    "miners",
			Required: true,
		},
		cmd.FlagRepo,
	},
	Before: before,
	Action: func(cctx *cli.Context) error {

		n, err := clinode.Setup(cctx.String(cmd.FlagRepo.Name))
		if err != nil {
			return err
		}

		tmax := cctx.Int64("term-max")

		// Tmax can't be more than policy max
		if tmax > verifreg13types.MaximumVerifiedAllocationTerm {
			return fmt.Errorf("specified term-max %d is larger than %d maximum allowed by verified regirty actor policy", tmax, verifreg13types.MaximumVerifiedAllocationTerm)
		}

		gapi, closer, err := lcli.GetGatewayAPI(cctx)
		if err != nil {
			return fmt.Errorf("can't setup gateway connection: %w", err)
		}
		defer closer()

		ctx := bcli.ReqContext(cctx)

		ldnMap, err := GetLdnMap(ctx, gapi)
		if err != nil {
			return fmt.Errorf("can't GetLdnMap: %w", err)
		}

		log.Infow("get ldnMap", "ldnMap", ldnMap)

		miners := cctx.StringSlice("miners")

		claimGroups := map[address.Address][]TxdcClaimInfo{}
		for _, miner := range miners {
			log.Infow("process miner", "miner", miner)
			maddr, err := address.NewFromString(miner)
			if err != nil {
				return fmt.Errorf("parsing miner %s: %w", miner, miner)
			}

			claims, err := gapi.StateGetClaims(ctx, maddr, types.EmptyTSK)
			if err != nil {
				return fmt.Errorf("getting claims for miner %s: %w", miner, err)
			}

			log.Infow("StateGetClaims", "miner", miner, "claims.len", len(claims))

			for cid, c := range claims {
				if w, ok := ldnMap[c.Client]; ok {
					claimGroups[w] = append(claimGroups[w], TxdcClaimInfo{
						ClaimId:      verifreg13types.ClaimId(cid),
						Client:       c.Client,
						ProviderAddr: maddr,
						ProviderId:   c.Provider,
						SectorNumber: c.Sector,
						TermMax:      c.TermMax,
						TermMin:      c.TermMin,
						TermStart:    c.TermStart,
					})
				}
			}
		}

		var msgs []*types.Message
		for w, claims := range claimGroups {
			log.Infow("claimGroups", "w", w, "claims.len", len(claims))
			msgsForWallet, err := createExtendClaimMsgForTxdc(ctx, gapi, claims, w, abi.ChainEpoch(tmax), cctx.Int("batch-size"))
			if err != nil {
				return err
			}
			msgs = append(msgs, msgsForWallet...)
		}

		// If not msgs are found then no claims can be extended
		if msgs == nil {
			fmt.Println("No eligible claims found")
			return nil
		}

		var mcids []cid.Cid
		ds := ds_sync.MutexWrap(datastore.NewMapDatastore())
		msgCount := len(msgs)
		for i, msg := range msgs {
			log.Infof("about to send msg (%d/%d): %w", i, msgCount, msg.Cid())

			mcid, sent, err := lib.TxdcSignAndPushToMpool(cctx, ctx, gapi, n, ds, msg)
			if err != nil {
				return err
			}
			if !sent {
				fmt.Printf("message %s with method %s not sent\n", msg.Cid(), msg.Method.String())
				continue
			}

			log.Infof("about to send msg (%d/%d): %s", i, msgCount, mcid.String())
			//wait, err := gapi.StateWaitMsg(ctx, mcid, uint64(cctx.Int("confidence")), 2000, true)
			//if err != nil {
			//	return fmt.Errorf("timeout waiting for message to land on chain %s", mcid.String())
			//
			//}
			//
			//if wait.Receipt.ExitCode.IsError() {
			//	log.Errorf("failed to execute message %s: %w", mcid.String(), wait.Receipt.ExitCode)
			//	continue
			//}
			log.Infof("msg sent (%d/%d): %s", i, msgCount, mcid.String())
			mcids = append(mcids, mcid)
			//break
		}

		// wait for msgs to get mined into a block
		eg := errgroup.Group{}
		eg.SetLimit(10)
		for _, msg := range mcids {
			m := msg
			eg.Go(func() error {
				wait, err := gapi.StateWaitMsg(ctx, m, uint64(cctx.Int("confidence")), 2000, true)
				if err != nil {
					return fmt.Errorf("timeout waiting for message to land on chain %s", m.String())

				}

				if wait.Receipt.ExitCode.IsError() {
					return fmt.Errorf("failed to execute message %s: %w", m.String(), wait.Receipt.ExitCode)
				}
				return nil
			})
		}
		return eg.Wait()
	},
}

func readClaimsFromFile(filePath string) (map[verifreg13types.ClaimId]TxClaimInfo, error) {
	txClaimInfos := map[verifreg13types.ClaimId]TxClaimInfo{}
	if filePath == "" {
		return txClaimInfos, nil
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		//SP,ClaimID,Sector
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "SP,ClaimID,Sector") {
			continue
		}

		parts := strings.Split(line, ",")
		if len(parts) < 3 {
			return nil, xerrors.Errorf("parts < 3: %s", line)
		}

		//sp
		spStr := parts[0]
		maddr, err := address.NewFromString(spStr)
		if err != nil {
			return nil, fmt.Errorf("parsing miner %s: %w", spStr, err)
		}
		mid, err := address.IDFromAddress(maddr)
		if err != nil {
			return nil, fmt.Errorf("converting miner address to miner ID: %w", err)
		}

		//claimId
		claimIdInt, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return nil, err
		}
		claimId := verifreg13types.ClaimId(claimIdInt)

		//sector number
		sectorNumberInt, err := strconv.ParseUint(parts[2], 10, 64)
		if err != nil {
			return nil, err
		}
		sectorNumber := abi.SectorNumber(sectorNumberInt)

		txClaimInfo := TxClaimInfo{
			ClaimId:      claimId,
			ProviderAddr: maddr,
			ProviderId:   abi.ActorID(mid),
			SectorNumber: sectorNumber,
		}
		txClaimInfos[claimId] = txClaimInfo
	}
	return txClaimInfos, nil
}

func createExtendClaimMsg(ctx context.Context, api api.Gateway, pcm map[verifreg13types.ClaimId]TxClaimInfo, wallet address.Address, tmax abi.ChainEpoch, batchSize int) ([]*types.Message, error) {
	ac, err := api.StateLookupID(ctx, wallet, types.EmptyTSK)
	if err != nil {
		return nil, err
	}
	w, err := address.IDFromAddress(ac)
	if err != nil {
		return nil, fmt.Errorf("converting wallet address to ID: %w", err)
	}

	wid := abi.ActorID(w)

	head, err := api.ChainHead(ctx)
	if err != nil {
		return nil, err
	}

	var terms []verifreg13types.ClaimTerm
	newClaims := make(map[verifreg13types.ClaimExtensionRequest]big.Int)
	rDataCap := big.NewInt(0)

	//{
	//	txClaimInfo1 := maps.Values(pcm)[0]
	//	claims, err := api.StateGetClaims(ctx, txClaimInfo1.ProviderAddr, types.EmptyTSK)
	//	if err != nil {
	//		return nil, fmt.Errorf("getting claims for miner %s: %w", txClaimInfo1.ProviderAddr, err)
	//	}
	//
	//	for _, txClaim := range pcm {
	//		var sectorClaims []verifreg13types.ClaimId
	//		for cID, c := range claims {
	//			if c.Sector == txClaim.SectorNumber {
	//				sectorClaims = append(sectorClaims, verifreg13types.ClaimId(cID))
	//				//if verifreg13types.ClaimId(cID) != txClaim.ClaimId {
	//				//	log.Warnw("verifreg13types.ClaimId(cID) != txClaim.ClaimId", "cID", cID, "txClaim.ClaimId", txClaim.ClaimId)
	//				//	break
	//				//}
	//			}
	//		}
	//		if len(sectorClaims) > 1 {
	//			log.Warnw("len(sectorClaims) > 1", "sectorClaims", sectorClaims)
	//		}
	//	}
	//}

	for claimID, txClaimInfo := range pcm {
		claimID := claimID
		claim, err := api.StateGetClaim(ctx, txClaimInfo.ProviderAddr, verifregtypes.ClaimId(claimID), types.EmptyTSK)
		if err != nil {
			return nil, fmt.Errorf("could not load the claim %d: %w", claimID, err)
		}
		if claim == nil {
			return nil, fmt.Errorf("claim %d not found for provider %s", claimID, txClaimInfo.ProviderAddr)
		}
		if claim.TermMax >= verifreg13types.MaximumVerifiedAllocationTerm {
			log.Warnw("claim.TermMax >= tmax")
			continue
		}

		// If the client is not the original client - burn datacap
		if claim.Client != wid {
			// The new duration should be greater than the original deal duration and claim should not already be expired
			if head.Height()+tmax-claim.TermStart > claim.TermMax && claim.TermStart+claim.TermMax > head.Height() {
				req := verifreg13types.ClaimExtensionRequest{
					Claim:    claimID,
					TermMax:  head.Height() + tmax - claim.TermStart,
					Provider: txClaimInfo.ProviderId,
				}
				newClaims[req] = big.NewInt(int64(claim.Size))
				rDataCap.Add(big.NewInt(int64(claim.Size)).Int, rDataCap.Int)
			} else {
				// If new duration shorter than the original duration
				log.Warnf("new duration shorter than the original duratio. claim=%d, provider=%s, head.Height()=%d, tmax=%d, claim.TermStart=%d, claim.TermMax=%d",
					claimID, txClaimInfo.ProviderAddr, head.Height(), tmax, claim.TermStart, claim.TermMax)
			}
			continue
		}
		// For original client, compare duration(TermMax) and claim should not already be expired
		if claim.TermMax < tmax && claim.TermStart+claim.TermMax > head.Height() {
			terms = append(terms, verifreg13types.ClaimTerm{
				ClaimId:  claimID,
				TermMax:  tmax,
				Provider: txClaimInfo.ProviderId,
			})
		}
	}

	var msgs []*types.Message

	if len(terms) > 0 {
		for i := 0; i < len(terms); i += batchSize {
			batchEnd := i + batchSize
			if batchEnd > len(terms) {
				batchEnd = len(terms)
			}

			batch := terms[i:batchEnd]

			params, err := actors.SerializeParams(&verifreg13types.ExtendClaimTermsParams{
				Terms: batch,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to searialise the parameters: %w", err)
			}
			gasPremium := types.NewInt(110000)
			oclaimMsg := &types.Message{
				To:         verifreg.Address,
				From:       wallet,
				Method:     verifreg.Methods.ExtendClaimTerms,
				Params:     params,
				GasPremium: gasPremium,
			}
			msgs = append(msgs, oclaimMsg)
		}
	}

	if len(newClaims) > 0 {
		// Get datacap balance
		aDataCap, err := api.StateVerifiedClientStatus(ctx, wallet, types.EmptyTSK)
		if err != nil {
			return nil, err
		}

		if aDataCap == nil {
			return nil, fmt.Errorf("wallet %s does not have any datacap", wallet)
		}

		// Check that we have enough data cap to make the allocation
		if rDataCap.GreaterThan(big.NewInt(aDataCap.Int64())) {
			return nil, fmt.Errorf("requested datacap %s is greater then the available datacap %s", rDataCap, aDataCap)
		}

		// Create a map of just keys, so we can easily batch based on the numeric keys
		keys := make([]verifreg13types.ClaimExtensionRequest, 0, len(newClaims))
		for k := range newClaims {
			keys = append(keys, k)
		}

		// Batch in 500 to avoid running out of gas
		for i := 0; i < len(keys); i += batchSize {
			batchEnd := i + batchSize
			if batchEnd > len(keys) {
				batchEnd = len(keys)
			}

			batch := keys[i:batchEnd]

			// Calculate Datacap for this batch
			dcap := big.NewInt(0)
			for _, k := range batch {
				dc := newClaims[k]
				dcap.Add(dcap.Int, dc.Int)
			}

			ncparams, err := actors.SerializeParams(&verifreg13types.AllocationRequests{
				Extensions: batch,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to searialise the parameters: %w", err)
			}

			transferParams, err := actors.SerializeParams(&datacap.TransferParams{
				To:           builtin.VerifiedRegistryActorAddr,
				Amount:       big.Mul(dcap, builtin.TokenPrecision),
				OperatorData: ncparams,
			})

			if err != nil {
				return nil, fmt.Errorf("failed to serialize transfer parameters: %w", err)
			}

			gasPremium := types.NewInt(110000)
			nclaimMsg := &types.Message{
				To:         builtin.DatacapActorAddr,
				From:       wallet,
				Method:     datacap2.Methods.TransferExported,
				Params:     transferParams,
				Value:      big.Zero(),
				GasPremium: gasPremium,
			}
			msgs = append(msgs, nclaimMsg)
		}
	}

	return msgs, nil
}

func createExtendClaimMsgForTxdc(ctx context.Context, api api.Gateway, claims []TxdcClaimInfo, wallet address.Address, tmax abi.ChainEpoch, batchSize int) ([]*types.Message, error) {
	head, err := api.ChainHead(ctx)
	if err != nil {
		return nil, err
	}

	var terms []verifreg13types.ClaimTerm

	for _, claim := range claims {
		claimID := claim.ClaimId

		if claim.TermMax >= verifreg13types.MaximumVerifiedAllocationTerm {
			log.Warnw("claim.TermMax >= tmax")
			continue
		}

		// compare duration(TermMax) and claim should not already be expired
		if claim.TermMax < tmax && claim.TermStart+claim.TermMax > head.Height() {
			terms = append(terms, verifreg13types.ClaimTerm{
				ClaimId:  claimID,
				TermMax:  tmax,
				Provider: claim.ProviderId,
			})
		}
	}

	var msgs []*types.Message

	if len(terms) > 0 {
		for i := 0; i < len(terms); i += batchSize {
			batchEnd := i + batchSize
			if batchEnd > len(terms) {
				batchEnd = len(terms)
			}

			batch := terms[i:batchEnd]

			params, err := actors.SerializeParams(&verifreg13types.ExtendClaimTermsParams{
				Terms: batch,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to searialise the parameters: %w", err)
			}
			gasPremium := types.NewInt(120000)
			oclaimMsg := &types.Message{
				To:         verifreg.Address,
				From:       wallet,
				Method:     verifreg.Methods.ExtendClaimTerms,
				Params:     params,
				GasPremium: gasPremium,
			}
			msgs = append(msgs, oclaimMsg)
		}
	}

	return msgs, nil
}

func GetLdnMap(ctx context.Context, api api.Gateway) (map[abi.ActorID]address.Address, error) {
	ldnMap := map[abi.ActorID]address.Address{}
	for _, addr := range share.TxLdnAddrMap {
		w, err := address.NewFromString(addr)
		if err != nil {
			return nil, err
		}

		ac, err := api.StateLookupID(ctx, w, types.EmptyTSK)
		if err != nil {
			return nil, err
		}

		id, err := address.IDFromAddress(ac)
		if err != nil {
			return nil, fmt.Errorf("converting wallet address to ID: %w", err)
		}

		wid := abi.ActorID(id)
		ldnMap[wid] = w
	}
	return ldnMap, nil
}

type TxClaimInfo struct {
	ClaimId      verifreg13types.ClaimId
	ProviderAddr address.Address
	ProviderId   abi.ActorID
	SectorNumber abi.SectorNumber
}

type TxdcClaimInfo struct {
	ClaimId      verifreg13types.ClaimId
	Client       abi.ActorID
	ProviderAddr address.Address
	ProviderId   abi.ActorID
	SectorNumber abi.SectorNumber
	TermMax      abi.ChainEpoch
	TermMin      abi.ChainEpoch
	TermStart    abi.ChainEpoch
}

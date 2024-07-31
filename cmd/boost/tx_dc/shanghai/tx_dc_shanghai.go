package shanghai

import (
	"bufio"
	"fmt"
	"github.com/filecoin-project/boost/cmd/boost/tx_dc/share"
	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/builtin/v9/market"
	"github.com/google/uuid"
	"github.com/ipfs/go-cid"
	"github.com/solopine/txcar/txcar"
	"golang.org/x/xerrors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type TxDcClientHandler struct {
	dealFilePath string
	dataVersion  txcar.Version
	provider     string
	ldnAddr      string
	count        int
	dealCars     []CustomTxDealCar
}

type CustomTxDealCar struct {
	dealCar    share.TxDealCar
	FolderName string
	FileName   string
}

func NewTxDcClientHandler(dealFilePath string, provider string) (*TxDcClientHandler, error) {
	dataVersion, providerOfFile, count, err := parseDealInfoFromPath(dealFilePath)
	if err != nil {
		return nil, err
	}

	if providerOfFile != provider {
		return nil, xerrors.Errorf("providerOfFile (%s) != provider (%s)", providerOfFile, provider)
	}

	dealCars, err := readTxDealCarsFromDealFile(dealFilePath)
	if err != nil {
		return nil, err
	}

	handler := TxDcClientHandler{
		dealFilePath: dealFilePath,
		dataVersion:  dataVersion,
		provider:     provider,
		ldnAddr:      "",
		count:        count,
		dealCars:     dealCars,
	}
	if !handler.checkValid() {
		return nil, xerrors.Errorf("checkValid false")
	}

	handler.ldnAddr, err = share.GetLdnAddrByTxVersion(dataVersion)
	if err != nil {
		return nil, err
	}

	return &handler, nil
}

func (p TxDcClientHandler) DataVersion() txcar.Version {
	return p.dataVersion
}
func (p TxDcClientHandler) LdnAddr() string {
	return p.ldnAddr
}

func (p TxDcClientHandler) Provider() string {
	return p.provider
}
func (p TxDcClientHandler) Count() int {
	return p.count
}

func (p TxDcClientHandler) DealCar(i int) share.TxDealCar {
	return p.dealCars[i].dealCar
}

func (p TxDcClientHandler) OutputFileName() string {
	return fmt.Sprintf("deal-shanghai-%d_%s_%d", p.dataVersion, p.provider, p.count)
}

func (p TxDcClientHandler) OutputHeader() string {
	return "dealUuid,maddr,payload cid,PieceCID,StartEpoch,EndEpoch,foldername,filename\n"
}

func (p TxDcClientHandler) OutputLine(i int, dealUuid uuid.UUID, maddr address.Address, rootCid cid.Cid, dealProposal *market.ClientDealProposal) string {
	return fmt.Sprintf("%s,%s,%s,%s,%d,%d,%s,%s\n",
		dealUuid, maddr, rootCid, dealProposal.Proposal.PieceCID, dealProposal.Proposal.StartEpoch, dealProposal.Proposal.EndEpoch, p.dealCars[i].FolderName, p.dealCars[i].FileName)
}

func (p TxDcClientHandler) checkValid() bool {
	if p.count != len(p.dealCars) {
		return false
	}
	if len(p.dealCars) < 1 {
		return false
	}

	for _, car := range p.dealCars {
		if car.dealCar.TxVersion != p.dataVersion {
			return false
		}
	}
	return true
}

func parseDealInfoFromPath(dealFilePath string) (dataVersion txcar.Version, provider string, count int, err error) {
	// 1001_f03513151_1557.csv
	if dealFilePath == "" {
		err = xerrors.Errorf("dealFilePath is empty: %s", dealFilePath)
		return
	}

	if filepath.Ext(dealFilePath) != ".csv" {
		err = xerrors.Errorf("fullNameParts is not csv: %s", dealFilePath)
		return
	}

	fullName := filepath.Base(dealFilePath)
	fileName := strings.TrimSuffix(fullName, filepath.Ext(fullName))

	fileNameParts := strings.Split(fileName, "_")
	dataVersionInt, err := strconv.Atoi(fileNameParts[0])
	if err != nil {
		return
	}
	dataVersion = txcar.Version(dataVersionInt)

	provider = fileNameParts[1]
	count, err = strconv.Atoi(fileNameParts[2])
	if err != nil {
		return
	}
	return
}

func readTxDealCarsFromDealFile(dealFilePath string) ([]CustomTxDealCar, error) {
	if dealFilePath == "" {
		return []CustomTxDealCar{}, nil
	}

	file, err := os.Open(dealFilePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var txDealCars []CustomTxDealCar
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		//deal_cid,foldername,filename,data_cid,piece_cid,client,piece_size,car_size
		line := scanner.Text()
		if strings.HasPrefix(line, "deal_cid") {
			continue
		}

		parts := strings.Split(line, ",")
		if len(parts) != 8 {
			return nil, xerrors.Errorf("parts is not 8: %s", line)
		}

		clientDataVersion, err := strconv.ParseInt(parts[5], 10, 64)
		if err != nil {
			return nil, err
		}
		txVersion := txcar.Version(clientDataVersion)

		pieceSize, err := strconv.ParseInt(parts[6], 10, 64)
		if err != nil {
			return nil, err
		}

		txDealCar := CustomTxDealCar{
			dealCar: share.TxDealCar{
				TxVersion: txVersion,
				PieceCid:  cid.MustParse(parts[4]),
				PieceSize: abi.PaddedPieceSize(pieceSize),
				RootCid:   cid.MustParse(parts[3]),
			},
			FolderName: parts[1],
			FileName:   parts[2],
		}
		txDealCars = append(txDealCars, txDealCar)
	}
	return txDealCars, nil
}

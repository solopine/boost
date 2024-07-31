package me

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
	dealFilePath  string
	dealFileName  string
	carFolderName string
	clientName    string
	carRootDir    string
	dataVersion   txcar.Version
	provider      string
	ldnAddr       string
	count         int
	dealCars      []CustomTxDealCar
}

type CustomTxDealCar struct {
	dealCar share.TxDealCar
}

func NewTxDcClientHandler(dealFilePath string, provider string, clientName string, carRootDir string) (*TxDcClientHandler, error) {
	dataVersion, dealFileName, carFolderName, err := parseDealInfoFromPath(dealFilePath)
	if err != nil {
		return nil, err
	}

	dealCars, err := readTxDealCarsFromDealFile(dealFilePath, dataVersion)
	if err != nil {
		return nil, err
	}

	handler := TxDcClientHandler{
		dealFilePath:  dealFilePath,
		dealFileName:  dealFileName,
		carFolderName: carFolderName,
		clientName:    clientName,
		carRootDir:    carRootDir,
		dataVersion:   dataVersion,
		provider:      provider,
		ldnAddr:       "",
		count:         len(dealCars),
		dealCars:      dealCars,
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
	return fmt.Sprintf("deal-%s-%s-%s.csv", p.clientName, p.dealFileName, p.provider)
}

func (p TxDcClientHandler) OutputHeader() string {
	return "dealUuid,sp,payloadCid,pieceCid,startEpoch,endEpoch,batch,carPath\n"
}

func (p TxDcClientHandler) OutputLine(i int, dealUuid uuid.UUID, maddr address.Address, rootCid cid.Cid, dealProposal *market.ClientDealProposal) string {
	carFileName := dealProposal.Proposal.PieceCID.String() + ".car"
	carFilePath := filepath.Join(p.carRootDir, p.carFolderName, carFileName)
	//importCmd := fmt.Sprintf("boostd import-data %s %s", dealUuid, carFilePath)
	return fmt.Sprintf("%s,%s,%s,%s,%d,%d,%s,%s\n",
		dealUuid, maddr, rootCid, dealProposal.Proposal.PieceCID, dealProposal.Proposal.StartEpoch, dealProposal.Proposal.EndEpoch,
		p.carFolderName, carFilePath,
	)
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

func parseDealInfoFromPath(dealFilePath string) (dataVersion txcar.Version, dealFileName string, carFolderName string, err error) {
	// 1004-02-001.2025-03-29_04-52-57.log
	// carFolderName is 1004-02-001
	if dealFilePath == "" {
		err = xerrors.Errorf("dealFilePath is empty: %s", dealFilePath)
		return
	}

	if filepath.Ext(dealFilePath) != ".log" {
		err = xerrors.Errorf("fullNameParts is not log: %s", dealFilePath)
		return
	}

	fullName := filepath.Base(dealFilePath)
	fileName := strings.TrimSuffix(fullName, filepath.Ext(fullName))
	dealFileName = fileName

	dataVersionInt, err := strconv.Atoi(fileName[0:4])
	if err != nil {
		return
	}
	dataVersion = txcar.Version(dataVersionInt)

	carFolderName = fileName[0:11]
	return
}

func readTxDealCarsFromDealFile(dealFilePath string, txVersion txcar.Version) ([]CustomTxDealCar, error) {
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
		//no	version	carKey	pieceCid	pieceSize	carSize	payloadCid
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Split(line, "\t")
		if len(parts) != 7 {
			return nil, xerrors.Errorf("parts is not 7: %s", line)
		}

		pieceSize, err := strconv.ParseInt(parts[4], 10, 64)
		if err != nil {
			return nil, err
		}

		txDealCar := CustomTxDealCar{
			dealCar: share.TxDealCar{
				TxVersion: txVersion,
				PieceCid:  cid.MustParse(parts[3]),
				PieceSize: abi.PaddedPieceSize(pieceSize),
				RootCid:   cid.MustParse(parts[6]),
			},
		}
		txDealCars = append(txDealCars, txDealCar)
	}
	return txDealCars, nil
}

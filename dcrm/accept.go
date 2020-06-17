package dcrm

import (
	"encoding/json"

	"github.com/fsn-dev/crossChain-Bridge/common"
)

// DoAcceptSign accept sign
func DoAcceptSign(keyID string, agreeResult string, msgHash []string, msgContext []string) (string, error) {
	nonce := uint64(0)
	data := AcceptData{
		TxType:     "ACCEPTSIGN",
		Key:        keyID,
		Accept:     agreeResult,
		MsgHash:    msgHash,
		MsgContext: msgContext,
		TimeStamp:  common.NowMilliStr(),
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	rawTX, err := BuildDcrmRawTx(nonce, payload)
	if err != nil {
		return "", err
	}
	return AcceptSign(rawTX)
}
